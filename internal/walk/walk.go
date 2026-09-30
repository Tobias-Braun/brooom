// Package walk provides the fast, parallel filesystem traversal the file
// detectors and discovery use.
//
// Design goals: feel instant on machines with hundreds of repositories.
//   - Parallel directory reading with a bounded worker pool fed by an
//     unbounded work queue, so deep or wide trees cannot deadlock it and no
//     goroutine is spawned per directory.
//   - Skip lists: .git is always visited but never descended into, and
//     Options.SkipNames adds more such names (matched case-insensitively on
//     Windows and macOS). Callers can prune any directory via the visit
//     callback.
//   - Never follow symlinks. Symlinks, Windows junctions and other reparse
//     points are reported as entries whose IsDir is false and are never
//     descended into. Walk does not resolve the root: callers pass a path
//     already resolved by scope.Guard.
//   - Unreadable directories are reported through the error callback and
//     skipped; they never abort a walk.
//   - Recursive directory sizes computed once and cached in a directory the
//     caller names (Options.CacheDir, normally ~/.brooom/cache), one record
//     per directory invalidated by that directory's mtime.
//
// Size semantics: Entry.Size is the logical file size. Entry.Allocated and
// DirSummary.SizeBytes are what deleting would actually free: on unix the
// allocated blocks (Stat_t.Blocks*512), on Windows the logical size because
// the allocated size is not cheaply available. Hard links are counted once
// per DirSize call.
//
// NewestModTime from a cached (non-Fresh) DirSize is a lower-bound hint.
// Callers that use it for age thresholds, recently_modified or LastModified
// of paths whose files may be modified in place (logs, transcripts, caches,
// ignored data) must pass Fresh: true. Sizing and ranking may use the cache.
// File sizes inside directories whose mtime did not change may likewise be
// stale (in-place appends do not change the directory mtime), so cached
// SizeBytes is suitable for sizing and ranking only. Anything a detector
// reports as a finding is re-stat'ed with Stat so findings never carry stale
// values.
//
// The package performs no writes except the cache file below CacheDir.
package walk

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Entry is one filesystem entry seen during a walk.
type Entry struct {
	// Path is the absolute path (filepath.Join(root, Rel)).
	Path string
	// Rel is the path relative to the walk root, with forward slashes. It
	// is empty for entries returned by Stat.
	Rel string
	// Name is the base name.
	Name string
	// Type is the entry's type bits (fs.ModeDir, fs.ModeSymlink, ...).
	Type fs.FileMode
	// Size is the logical file size (0 for directories; see DirSize).
	Size int64
	// Allocated is the number of bytes actually allocated on disk. On unix
	// it is Stat_t.Blocks*512 taken from the same Info call as Size. On
	// Windows it equals Size because the allocated size is not cheaply
	// available. It is 0 for directories.
	Allocated int64
	// ModTime is the entry's modification time.
	ModTime time.Time
	// Depth is the number of path elements below the root (children of the
	// root have depth 1; Stat reports 0).
	Depth int

	// fid identifies the file for hard link detection (unix only).
	fid fileID
}

// IsDir reports whether the entry is a directory (never true for symlinks,
// junctions or other reparse points).
func (e Entry) IsDir() bool { return e.Type.IsDir() }

// IsSymlink reports whether the entry is a symlink.
func (e Entry) IsSymlink() bool { return e.Type&fs.ModeSymlink != 0 }

// Decision tells the walker how to continue after visiting an entry.
type Decision int

const (
	// Continue descends into directories normally.
	Continue Decision = iota
	// SkipDir does not descend into this directory. The return value of
	// visit is ignored for non-directories.
	SkipDir
)

// Options configures a walk or a DirSize computation.
type Options struct {
	// Concurrency is the number of parallel directory readers (<= 0 means
	// runtime.NumCPU()).
	Concurrency int
	// SkipNames are directory base names that are visited but never
	// descended into. Matching is exact on unix and case-insensitive on
	// Windows and macOS. Only Walk honours it; DirSize always sums the
	// whole tree.
	SkipNames []string
	// MaxDepth limits descent: entries with Depth <= MaxDepth are reported
	// and directories at Depth == MaxDepth are not descended (0 = unlimited).
	// Only Walk honours it.
	MaxDepth int
	// CacheDir enables the DirSize cache when non-empty. It is the
	// directory the cache files are written to; walk does not import
	// config, callers pass config.ResolveDirs().Cache (via detect.Env).
	CacheDir string
	// Fresh makes DirSize ignore cached directory records and re-read every
	// directory, while still refreshing the cache. NewestModTime from a
	// cached (non-Fresh) DirSize is a lower-bound hint. Callers that use it
	// for age thresholds, recently_modified or LastModified of paths whose
	// files may be modified in place (logs, transcripts, caches, ignored
	// data) must pass Fresh: true. Sizing and ranking may use the cache.
	Fresh bool
}

// workers returns the effective pool size.
func (o Options) workers() int {
	if o.Concurrency > 0 {
		return o.Concurrency
	}
	return max(runtime.NumCPU(), 1)
}

// isGitName reports whether an entry name is ".git" on this OS's filesystem.
func isGitName(name string) bool {
	return name == ".git" || (foldNames() && strings.EqualFold(name, ".git"))
}

// foldNames reports whether directory names compare case-insensitively on
// the current OS (the default filesystems of Windows and macOS).
func foldNames() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }

// nameMatcher returns a predicate for names that must not be descended into:
// the built-in .git plus the given names.
func nameMatcher(names []string) func(string) bool {
	all := append([]string{".git"}, names...)
	fold := foldNames()
	return func(name string) bool {
		for _, n := range all {
			if n == name || (fold && strings.EqualFold(n, name)) {
				return true
			}
		}
		return false
	}
}

// dirTask is one directory queued for reading.
type dirTask struct {
	path  string
	rel   string
	depth int
}

// walker holds the state shared by the workers of one Walk call.
type walker struct {
	ctx   context.Context
	opts  Options
	skip  func(string) bool
	visit func(Entry) Decision
	onErr func(path string, err error)
}

// Walk traverses root in parallel and calls visit for every entry except the
// root itself. root is made absolute but symlinks are not resolved; a
// symlink to a directory counts as not a directory, so callers resolve first
// via scope.Guard. visit may be called concurrently from multiple goroutines
// and sibling order is unspecified. onErr is called for unreadable entries
// (may be nil, may be called concurrently). When ctx is cancelled Walk stops
// promptly and returns ctx.Err().
func Walk(ctx context.Context, root string, opts Options, visit func(Entry) Decision, onErr func(path string, err error)) error {
	abs, err := requireDir(root)
	if err != nil {
		return err
	}
	if onErr == nil {
		onErr = func(string, error) {}
	}
	w := &walker{ctx: ctx, opts: opts, skip: nameMatcher(opts.SkipNames), visit: visit, onErr: onErr}
	return runPool(ctx, opts.workers(), dirTask{path: abs}, w.process)
}

// requireDir makes root absolute and verifies it is a real directory.
func requireDir(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("walk: %s: %w", root, err)
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("walk: %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("walk: %s is not a directory", abs)
	}
	return abs, nil
}

// process reads one directory, reports its entries and queues the
// subdirectories to descend into.
func (w *walker) process(t dirTask, submit func(dirTask)) {
	if w.ctx.Err() != nil {
		return
	}
	entries, err := readDir(t.path, w.onErr)
	if err != nil {
		w.onErr(t.path, err)
	}
	for _, e := range entries {
		if w.ctx.Err() != nil {
			return
		}
		e.Depth = t.depth + 1
		e.Rel = joinRel(t.rel, e.Name)
		if w.visit(e) == SkipDir || !e.IsDir() || !w.descend(e) {
			continue
		}
		submit(dirTask{path: e.Path, rel: e.Rel, depth: e.Depth})
	}
}

// descend applies the skip list and the depth limit to a directory entry.
func (w *walker) descend(e Entry) bool {
	if w.skip(e.Name) {
		return false
	}
	return w.opts.MaxDepth <= 0 || e.Depth < w.opts.MaxDepth
}

// joinRel appends name to a forward-slash relative path.
func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

// Stat returns the entry for path without following symlinks. Rel is empty
// and Depth is 0. Detectors re-stat every path they report.
func Stat(path string) (Entry, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Entry{}, err
	}
	return entryFromInfo(abs, fi), nil
}

// entryFromInfo builds an Entry from lstat-style file info.
func entryFromInfo(path string, fi fs.FileInfo) Entry {
	typ := fi.Mode().Type()
	if typ&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		// Reparse points may carry the directory bit on some Go versions;
		// they must never look like descendable directories.
		typ &^= fs.ModeDir
	}
	e := Entry{Path: path, Name: fi.Name(), Type: typ, ModTime: fi.ModTime(), fid: fileIDOf(fi)}
	if typ.IsDir() {
		return e
	}
	e.Size = fi.Size()
	e.Allocated = allocatedSize(fi)
	return e
}

// DirSummary is the aggregate of a directory tree.
type DirSummary struct {
	// SizeBytes is the total allocated size of regular files below the
	// directory (file sizes on Windows). Symlinks count as their own size,
	// targets are not followed, hard links are counted once.
	SizeBytes int64
	// Files is the number of regular files (a hard-linked file counts once).
	Files int
	// NewestModTime is the newest mtime of any entry below the directory
	// (directories included). Unless the call used Options.Fresh it is a
	// lower-bound hint: see the package documentation.
	NewestModTime time.Time
	// HasGit is true when any directory of the tree (the root included)
	// directly contains an entry named ".git", be it a directory or a file
	// (linked worktrees and submodules). It is gathered by the size pass
	// itself and survives the cache, so callers that must refuse trees
	// holding a repository need no second traversal.
	HasGit bool
}
