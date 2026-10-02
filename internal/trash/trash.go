// Package trash moves files and directories to the OS trash (Windows Recycle
// Bin, macOS Trash, freedesktop trash on Linux/BSD; one implementation per OS
// in ostrash_<os>.go) and restores them.
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
	"time"
)

// ErrNotRestorable is returned by Restore for records that cannot be
// restored (permanently deleted, or the trashed copy is gone).
var ErrNotRestorable = errors.New("item cannot be restored")

// ErrRestoreConflict is returned when the original path exists again.
var ErrRestoreConflict = errors.New("original path already exists")

// Strategy names how a Record was removed. Brooom only moves to the OS
// trash; manifests of earlier releases may also hold "quarantine" (a copy
// below ~/.brooom/quarantine) or "delete" (no copy).
type Strategy string

// StrategyTrash is the OS trash.
const StrategyTrash Strategy = "trash"

// Record describes one removed item and how to restore it.
type Record struct {
	Strategy Strategy `json:"strategy"`
	// OriginalPath is the absolute path the item was removed from.
	OriginalPath string `json:"original_path"`
	// StoredPath is where the item now lives: the file inside the OS trash.
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

// Trasher removes and restores files.
type Trasher interface {
	// Remove disposes of path (file, directory or symlink) and returns a
	// record describing how to restore it. The caller has already validated
	// path through a scope.Guard. On Windows, files locked by another
	// process make Remove fail with an error naming the path; nothing is
	// partially removed where the platform allows avoiding it.
	Remove(ctx context.Context, path string) (Record, error)
	// Restore moves a removed item back to its original path. It fails with
	// ErrRestoreConflict if something exists there, and ErrNotRestorable if
	// the stored copy is gone.
	Restore(ctx context.Context, r Record) error
}

// New returns the OS trasher of this platform (newOSTrasher in
// ostrash_<os>.go).
func New() (Trasher, error) {
	return newOSTrasher()
}
