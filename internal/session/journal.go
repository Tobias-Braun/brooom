package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// journalExt is the extension of the append-only entry journal that sits
// next to a manifest snapshot: <id>.journal.
//
// Rewriting and fsyncing the whole manifest after every entry made a large
// run quadratic in I/O. The executor now writes the manifest snapshot once
// when the run starts, appends one fsynced line per entry to the journal
// (constant cost, same per-entry durability) and folds everything into a
// fresh snapshot with Save when the run finishes. Readers (Load, List)
// replay the journal on top of the snapshot, so a crashed run still shows
// every entry that was recorded.
const journalExt = ".journal"

// journalRecord is one journal line. Index is the position of the entry in
// Manifest.Entries, which makes replay idempotent: a snapshot that already
// holds the entry (a crash between Save's rename and the journal removal)
// simply skips it.
type journalRecord struct {
	Index int   `json:"i"`
	Entry Entry `json:"entry"`
}

// AppendEntry durably appends e, the entry at position index of manifest id,
// to the journal. The manifest snapshot must already exist (Save it first).
// The line is compact JSON and fsynced, so a crash loses at most a torn
// final line, which replay ignores.
func (s *Store) AppendEntry(id string, index int, e Entry) error {
	if err := validID(id); err != nil {
		return err
	}
	line, err := json.Marshal(journalRecord{Index: index, Entry: e})
	if err != nil {
		return fmt.Errorf("encode journal entry of session %s: %w", id, err)
	}
	path := filepath.Join(s.Dir, id+journalExt)
	_, statErr := os.Lstat(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open session journal %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("append to session journal %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync session journal %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close session journal %s: %w", path, err)
	}
	if statErr != nil {
		// The journal was just created: make its directory entry durable.
		syncDir(s.Dir)
	}
	return nil
}

// replayJournal applies the journal at path to m. A missing journal is
// nothing to do. Entries the snapshot already holds are skipped, a gap is an
// error, and an unparsable final line without newline (a torn write from a
// crash) is ignored; any other unparsable line means real corruption.
func replayJournal(path string, m *Manifest) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read session journal %s: %w", path, err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	// Split yields a final element after the last newline: empty for a
	// well-formed journal, the torn remainder otherwise.
	last := len(lines) - 1
	applied := false
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec journalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			if i == last {
				break
			}
			return fmt.Errorf("corrupt session journal %s line %d: %w", path, i+1, err)
		}
		switch {
		case rec.Index < len(m.Entries):
			continue
		case rec.Index > len(m.Entries):
			return fmt.Errorf("corrupt session journal %s line %d: entry %d follows %d entries", path, i+1, rec.Index, len(m.Entries))
		}
		m.Entries = append(m.Entries, rec.Entry)
		applied = true
	}
	if applied {
		m.RecomputeReclaimed()
	}
	return nil
}
