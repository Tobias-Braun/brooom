// Package trash disposes of files and directories according to a strategy
// and restores them where possible.
//
// Three strategies share one interface:
//
//   - trash:      the OS trash (Windows Recycle Bin, macOS Trash, freedesktop
//     trash on Linux/BSD), one implementation per OS in trash_<os>.go
//   - quarantine: a move into ~/.brooom/quarantine/<session-id>/ that
//     `brooom purge` empties after the retention period
//   - delete:     immediate permanent deletion (explicit opt-in only)
//
// Every removal returns a Record that is stored in the session manifest so
// `brooom undo` can restore what is restorable. Implementations must never
// follow symlinks when removing: a symlink is removed as a link, its target
// is left alone.
package trash

import (
	"context"
	"errors"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// ErrNotRestorable is returned by Restore for records that cannot be
// restored (permanently deleted, or the trashed copy is gone).
var ErrNotRestorable = errors.New("item cannot be restored")

// ErrRestoreConflict is returned when the original path exists again.
var ErrRestoreConflict = errors.New("original path already exists")

// Record describes one removed item and how to restore it.
type Record struct {
	Strategy config.TrashStrategy `json:"strategy"`
	// OriginalPath is the absolute path the item was removed from.
	OriginalPath string `json:"original_path"`
	// StoredPath is where the item now lives: the file inside the OS trash
	// or quarantine directory. Empty for StrategyDelete.
	StoredPath string `json:"stored_path,omitempty"`
	// InfoPath is an OS trash metadata file (freedesktop .trashinfo,
	// Windows $I file) that must be removed on restore. Optional.
	InfoPath string `json:"info_path,omitempty"`
	// SizeBytes is the size of the removed item.
	SizeBytes int64 `json:"size_bytes"`
	// IsDir is true for directories.
	IsDir bool `json:"is_dir"`
	// RemovedAt is when the item was removed.
	RemovedAt time.Time `json:"removed_at"`
	// Restorable is true when Restore is expected to work.
	Restorable bool `json:"restorable"`
}

// Trasher removes and restores files with one strategy.
type Trasher interface {
	// Strategy returns the strategy this trasher implements.
	Strategy() config.TrashStrategy
	// Remove disposes of path (file, directory or symlink) and returns a
	// record describing how to restore it. The caller has already validated
	// path through a scope.Guard. On Windows, files locked by another
	// process make Remove fail with an error naming the path; nothing is
	// partially removed where the platform allows avoiding it.
	Remove(ctx context.Context, path string) (Record, error)
	// Restore moves a removed item back to its original path. It fails with
	// ErrRestoreConflict if something exists there, and ErrNotRestorable if
	// the stored copy is gone or the strategy was delete.
	Restore(ctx context.Context, r Record) error
}

// Options configures trasher construction.
type Options struct {
	// SessionID names the quarantine subdirectory.
	SessionID string
	// QuarantineDir is ~/.brooom/quarantine.
	QuarantineDir string
}

// errNotImplemented marks skeleton functions that are implemented by the
// milestone issues. It is never returned by a released binary.
var errNotImplemented = errors.New("trash: not implemented yet")

// New returns the trasher for a strategy.
func New(strategy config.TrashStrategy, opts Options) (Trasher, error) {
	return nil, errNotImplemented
}
