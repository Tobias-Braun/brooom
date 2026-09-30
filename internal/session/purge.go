package session

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// MarkPurged records that the given quarantine session directories were
// deleted: every applied entry whose quarantined copy lived inside one of
// them becomes non-restorable with a recovery hint, so `brooom undo` and
// `brooom sessions` stay truthful. Manifests are never deleted, only updated
// (saved atomically). It returns the number of entries changed; a manifest
// that cannot be saved is reported after the others were processed.
func (s *Store) MarkPurged(purgedDirs []string, at time.Time) (int, error) {
	if len(purgedDirs) == 0 {
		return 0, nil
	}
	list, _, err := s.List()
	if err != nil {
		return 0, err
	}
	total := 0
	var firstErr error
	for _, m := range list {
		n := markPurgedEntries(m, purgedDirs, at)
		if n == 0 {
			continue
		}
		if err := s.Save(m); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("record purge in session %s: %w", m.ID, err)
			continue
		}
		total += n
	}
	return total, firstErr
}

// markPurgedEntries updates the entries of m and returns how many changed.
func markPurgedEntries(m *Manifest, purgedDirs []string, at time.Time) int {
	n := 0
	for i := range m.Entries {
		e := &m.Entries[i]
		if e.Trash == nil || e.Trash.Strategy != config.StrategyQuarantine || e.Status != StatusApplied {
			continue
		}
		if !insideAny(purgedDirs, e.Trash.StoredPath) {
			continue
		}
		e.Restorable = false
		e.Trash.Restorable = false
		e.RecoveryHint = "purged from quarantine on " + at.Format("2006-01-02")
		n++
	}
	return n
}

// insideAny reports whether p lies strictly inside one of the directories,
// compared by path components.
func insideAny(dirs []string, p string) bool {
	if p == "" {
		return false
	}
	p = filepath.Clean(p)
	for _, d := range dirs {
		rel, err := filepath.Rel(filepath.Clean(d), p)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return true
	}
	return false
}
