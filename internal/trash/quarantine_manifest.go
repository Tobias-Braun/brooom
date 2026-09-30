package trash

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ManifestName is the file name of the per-session quarantine manifest,
// stored at <QuarantineDir>/<SessionID>/manifest.json. Its format is a
// stable contract: `brooom purge` reads it to list and expire sessions
// without consulting the session store.
const ManifestName = "manifest.json"

// JournalName is the append-only journal next to the manifest. Remove
// appends one compact JSON line per quarantined item instead of rewriting the
// whole manifest (which made large runs quadratic in I/O); every reader
// replays it on top of manifest.json, and a rewrite of the manifest
// (Restore) folds it in and removes it.
const JournalName = "manifest.journal"

// ManifestVersion is the current QuarantineManifest.Version.
const ManifestVersion = 1

// QuarantineManifest describes everything quarantined by one session.
type QuarantineManifest struct {
	Version   int              `json:"version"`
	SessionID string           `json:"session_id"`
	CreatedAt time.Time        `json:"created_at"`
	Items     []QuarantineItem `json:"items"`
}

// QuarantineItem is one quarantined path.
type QuarantineItem struct {
	// N is the per-session counter naming the item's directory.
	N int `json:"n"`
	// OriginalPath is where the item was removed from.
	OriginalPath string `json:"original_path"`
	// StoredPath is relative to the session directory ("<n>/<basename>"), so
	// the quarantine directory can be moved as a whole.
	StoredPath string    `json:"stored_path"`
	RemovedAt  time.Time `json:"removed_at"`
	SizeBytes  int64     `json:"size_bytes"`
	IsDir      bool      `json:"is_dir"`
	IsSymlink  bool      `json:"is_symlink"`
}

// readManifest loads the manifest in sessionDir. A missing file yields an
// empty manifest for sessionID created at now, so a fresh and a resumed
// session take the same path.
func readManifest(sessionDir, sessionID string, now time.Time) (*QuarantineManifest, error) {
	m, err := loadQuarantineManifest(sessionDir)
	if errors.Is(err, fs.ErrNotExist) {
		return &QuarantineManifest{Version: ManifestVersion, SessionID: sessionID, CreatedAt: now.UTC()}, nil
	}
	return m, err
}

// loadQuarantineManifest reads manifest.json and replays the journal,
// without the "missing means empty" behaviour of readManifest: listing must
// tell a missing manifest apart to apply its fallbacks. A missing manifest
// yields an error wrapping fs.ErrNotExist.
func loadQuarantineManifest(sessionDir string) (*QuarantineManifest, error) {
	path := filepath.Join(sessionDir, ManifestName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read quarantine manifest %q: %w", path, err)
	}
	var m QuarantineManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt quarantine manifest %q: %w", path, err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("quarantine manifest %q has unsupported version %d (this brooom understands %d)", path, m.Version, ManifestVersion)
	}
	if err := m.replayJournal(filepath.Join(sessionDir, JournalName)); err != nil {
		return nil, err
	}
	return &m, nil
}

// replayJournal adds the journaled items that the manifest does not hold yet
// (matched by counter, so replay is idempotent). An unparsable final line
// without newline is a torn write from a crash and is ignored; any other
// unparsable line is corruption.
func (m *QuarantineManifest) replayJournal(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read quarantine journal %q: %w", path, err)
	}
	known := make(map[int]bool, len(m.Items))
	for _, it := range m.Items {
		known[it.N] = true
	}
	lines := bytes.Split(data, []byte{'\n'})
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var it QuarantineItem
		if err := json.Unmarshal(line, &it); err != nil {
			if i == len(lines)-1 {
				break
			}
			return fmt.Errorf("corrupt quarantine journal %q line %d: %w", path, i+1, err)
		}
		if !known[it.N] {
			known[it.N] = true
			m.Items = append(m.Items, it)
		}
	}
	return nil
}

// appendItem durably appends one item to the journal in sessionDir: a single
// compact line, fsynced, so the cost per item does not grow with the number
// of items already recorded.
func appendItem(sessionDir string, it QuarantineItem) error {
	line, err := json.Marshal(it)
	if err != nil {
		return err
	}
	path := filepath.Join(sessionDir, JournalName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write quarantine journal in %q: %w", sessionDir, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("cannot write quarantine journal in %q: %w", sessionDir, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("cannot write quarantine journal in %q: %w", sessionDir, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot write quarantine journal in %q: %w", sessionDir, err)
	}
	return nil
}

// The read-modify-write cycle of a manifest is serialised only by the
// quarantine's in-process mutex. Two brooom processes writing to the same
// session at once could lose an entry; sessions are per process, so this is
// not expected in practice.

// writeManifest replaces the manifest in sessionDir atomically (temp file in
// the same directory, then rename) with mode 0600, so a crash never leaves a
// truncated manifest behind.
func writeManifest(sessionDir string, m *QuarantineManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(sessionDir, ManifestName+".tmp-*")
	if err != nil {
		return fmt.Errorf("cannot write quarantine manifest in %q: %w", sessionDir, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("cannot write quarantine manifest in %q: %w", sessionDir, err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmpName, filepath.Join(sessionDir, ManifestName)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cannot write quarantine manifest in %q: %w", sessionDir, err)
	}
	// The snapshot now holds every journaled item. A journal left behind
	// would replay items this write deliberately dropped (a restored item),
	// so a failed removal is an error.
	if err := os.Remove(filepath.Join(sessionDir, JournalName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot remove quarantine journal in %q: %w", sessionDir, err)
	}
	return nil
}

// nextN returns the next free item counter: one above the highest recorded
// item, so a restarted process continues where the previous one stopped.
func (m *QuarantineManifest) nextN() int {
	n := 0
	for _, it := range m.Items {
		if it.N > n {
			n = it.N
		}
	}
	return n + 1
}

// drop removes the item with counter n, if present.
func (m *QuarantineManifest) drop(n int) {
	kept := m.Items[:0]
	for _, it := range m.Items {
		if it.N != n {
			kept = append(kept, it)
		}
	}
	m.Items = kept
}
