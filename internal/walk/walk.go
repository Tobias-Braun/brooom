// Package walk provides the fast, parallel filesystem traversal the file
// detectors and discovery use.
//
// Design goals: feel instant on machines with hundreds of repositories.
//   - Parallel directory reading with a bounded worker pool.
//   - Skip lists: never descend into .git internals or configured skip dirs;
//     callers can prune any directory via the visit callback.
//   - Never follow directory symlinks; symlinks are reported as entries.
//   - Recursive directory sizes computed once and cached in
//     ~/.brooom/cache, invalidated by directory mtimes. Cached values are
//     only used for aggregate sizes: anything a detector reports as a
//     finding is re-stat'ed (Stat) so findings never carry stale mtimes.
//   - Unreadable directories are reported through the error callback and
//     skipped; they never abort a walk.
package walk

import (
	"context"
	"errors"
	"io/fs"
	"time"
)

// errNotImplemented marks skeleton functions that are implemented by the
// milestone issues. It is never returned by a released binary.
var errNotImplemented = errors.New("walk: not implemented yet")

// Entry is one filesystem entry seen during a walk.
type Entry struct {
	// Path is the absolute path.
	Path string
	// Rel is the path relative to the walk root, with forward slashes.
	Rel string
	// Name is the base name.
	Name string
	// Type is the entry's type bits (fs.ModeDir, fs.ModeSymlink, ...).
	Type fs.FileMode
	// Size is the file size (0 for directories; see DirSize).
	Size int64
	// ModTime is the entry's modification time.
	ModTime time.Time
	// Depth is the number of path elements below the root (root = 0).
	Depth int
}

// IsDir reports whether the entry is a directory (never true for symlinks).
func (e Entry) IsDir() bool { return e.Type.IsDir() }

// IsSymlink reports whether the entry is a symlink.
func (e Entry) IsSymlink() bool { return e.Type&fs.ModeSymlink != 0 }

// Decision tells the walker how to continue after visiting an entry.
type Decision int

const (
	// Continue descends into directories normally.
	Continue Decision = iota
	// SkipDir does not descend into this directory.
	SkipDir
)

// Options configures a walk.
type Options struct {
	// Concurrency is the number of parallel directory readers (0 = CPUs).
	Concurrency int
	// SkipNames are directory base names never descended into.
	SkipNames []string
	// MaxDepth limits descent (0 = unlimited).
	MaxDepth int
}

// Walk traverses root in parallel and calls visit for every entry except the
// root itself. visit may be called concurrently from multiple goroutines.
// onErr is called for unreadable entries (may be nil).
func Walk(ctx context.Context, root string, opts Options, visit func(Entry) Decision, onErr func(path string, err error)) error {
	return errNotImplemented
}

// DirSummary is the aggregate of a directory tree.
type DirSummary struct {
	// SizeBytes is the total size of regular files below the directory
	// (symlinks count as their own size, targets are not followed).
	SizeBytes int64
	// Files is the number of regular files.
	Files int
	// NewestModTime is the newest mtime of any entry below the directory.
	NewestModTime time.Time
}

// DirSize computes the summary of the directory tree at path, using the
// scan cache when enabled.
func DirSize(ctx context.Context, path string, opts Options) (DirSummary, error) {
	return DirSummary{}, errNotImplemented
}
