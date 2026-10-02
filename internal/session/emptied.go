package session

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/Tobias-Braun/brooom/internal/trash"
)

// MarkTrashEmptied records that the given OS trash copies were deleted
// permanently by `brooom empty-trash`: every applied entry whose stored copy
// is one of them becomes non-restorable with a recovery hint, so `brooom
// undo` and `brooom sessions` stay truthful. Manifests are never deleted, only
// updated (saved atomically). It returns the number of entries changed; a
// manifest that cannot be saved is reported after the others were processed.
func (s *Store) MarkTrashEmptied(stored []string, at time.Time) (int, error) {
	if len(stored) == 0 {
		return 0, nil
	}
	gone := map[string]bool{}
	for _, p := range stored {
		gone[filepath.Clean(p)] = true
	}
	list, _, err := s.List()
	if err != nil {
		return 0, err
	}
	total := 0
	var firstErr error
	for _, m := range list {
		n := 0
		for i := range m.Entries {
			e := &m.Entries[i]
			if e.Trash == nil || e.Trash.Strategy != trash.StrategyTrash || e.Status != StatusApplied || !gone[filepath.Clean(e.Trash.StoredPath)] {
				continue
			}
			e.Restorable = false
			e.Trash.Restorable = false
			e.RecoveryHint = "deleted from the trash on " + at.Format("2006-01-02")
			n++
		}
		if n == 0 {
			continue
		}
		if err := s.Save(m); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("record the emptied trash in session %s: %w", m.ID, err)
			continue
		}
		total += n
	}
	return total, firstErr
}
