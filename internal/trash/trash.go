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
//
// # macOS and Full Disk Access
//
// The macOS trasher hands items to NSFileManager (called directly through purego), which
// works without special permissions and returns the resulting path in the
// Trash. Since macOS 10.15, however, ~/.Trash is protected by TCC: a CLI
// without Full Disk Access gets "Operation not permitted" when it stats,
// lists or moves items inside the Trash. Trashing therefore succeeds while
// Restore (and any inspection of Record.StoredPath) may fail. Restore then
// returns an error wrapping ErrNotRestorable that says so; restore such items
// with Finder's "Put Back" or grant Full Disk Access to the terminal. Items
// moved by the ~/.Trash fallback (used when the native call is unavailable or fails
// for an item) have no Put Back metadata; only brooom's own undo restores them.
//
// The native call has no timeout of its own, so it runs under a deadline. A
// call that does not return in time is reported as an error saying it may still
// be pending: it is never treated as success and never followed by the
// ~/.Trash fallback, and the rest of the batch is skipped.
package trash

import (
	"context"
	"errors"
	"fmt"
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

// New returns the trasher for a strategy. The OS trash is implemented per
// platform in ostrash_<os>.go (newOSTrasher), quarantine in quarantine.go and
// permanent deletion in delete.go.
func New(strategy config.TrashStrategy, opts Options) (Trasher, error) {
	switch strategy {
	case config.StrategyTrash, "":
		return newOSTrasher(opts)
	case config.StrategyQuarantine:
		return newQuarantine(opts)
	case config.StrategyDelete:
		return newDeleter(opts)
	default:
		return nil, fmt.Errorf("unknown trash strategy %q (use trash, quarantine or delete)", strategy)
	}
}
