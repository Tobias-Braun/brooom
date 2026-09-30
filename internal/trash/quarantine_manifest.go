package trash

import (
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
	path := filepath.Join(sessionDir, ManifestName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &QuarantineManifest{Version: ManifestVersion, SessionID: sessionID, CreatedAt: now.UTC()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read quarantine manifest %q: %w", path, err)
	}
	var m QuarantineManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt quarantine manifest %q: %w", path, err)
	}
	return &m, nil
}

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
