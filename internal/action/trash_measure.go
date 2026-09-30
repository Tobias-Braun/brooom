package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// statRoot and lstatIsDir are seams for tests: they let a Linux test present
// the ModeDir|ModeIrregular reparse points Windows reports.
var (
	statRoot   = walk.Stat
	lstatIsDir = func(path string) bool {
		fi, err := os.Lstat(path)
		// Symlinks are removed as links and their target is never touched.
		return err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0
	}
)

// measurement is what one traversal of a trash target yields.
type measurement struct {
	size      int64
	newest    time.Time
	nestedGit string
}

// sizeAndNestedGit measures a target and looks for nested git repositories
// in one pass. Files and symlinks are sized from their own lstat data. A
// directory is walked once with Fresh options, following the same rules as
// walk.DirSize (links are never followed, hard links count once, symlinks
// count as their own size), so the size matches what the rest of Brooom
// reports for the same directory.
//
// walk.Walk visits `.git` entries (files and directories, so nested repos,
// linked worktrees and submodules alike) without descending into them, which
// means the nested-repository check costs no extra traversal. The reported
// nestedGit is the smallest relative path so the reason is deterministic
// although the walk is parallel. A directory that cannot be read completely
// is an error: an unreadable subtree could hide a repository.
func sizeAndNestedGit(ctx context.Context, path string) (measurement, error) {
	root, err := statRoot(path)
	if err != nil {
		return measurement{}, err
	}
	if !root.IsDir() {
		// A directory the walker does not treat as one (a junction or an
		// unclassifiable reparse point) has contents nobody inspected, so a
		// nested repository cannot be ruled out. Trashing it as a "file" of
		// size zero would be a guess; refuse instead.
		if lstatIsDir(path) {
			return measurement{}, errors.New("directory is a reparse point whose contents cannot be inspected, so a nested git repository cannot be ruled out")
		}
		size := root.Allocated
		if root.IsSymlink() {
			size = root.Size
		}
		return measurement{size: size, newest: root.ModTime}, nil
	}
	return measureTree(ctx, path, root.ModTime)
}

// treeMeter accumulates a walk; visit runs concurrently.
type treeMeter struct {
	mu    sync.Mutex
	m     measurement
	links map[string]struct{}
	errs  []error
	// ownGit is the relative path of a .git entry that belongs to the target
	// itself (the link file of a linked worktree) and is therefore not a
	// nested repository.
	ownGit string
}

func (t *treeMeter) visit(e walk.Entry) walk.Decision {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e.ModTime.After(t.m.newest) {
		t.m.newest = e.ModTime
	}
	if isGitName(e.Name) && e.Rel != t.ownGit && (t.m.nestedGit == "" || e.Rel < t.m.nestedGit) {
		t.m.nestedGit = e.Rel
	}
	t.m.size += t.entrySize(e)
	return walk.Continue
}

// entrySize is the contribution of one entry to the directory size, with the
// exact accounting of walk.DirSize.
func (t *treeMeter) entrySize(e walk.Entry) int64 {
	switch {
	case e.IsSymlink():
		return e.Size
	case !e.IsSizedFile():
		return 0
	}
	if id, ok := e.HardLinkID(); ok {
		if _, dup := t.links[id]; dup {
			return 0
		}
		t.links[id] = struct{}{}
	}
	return e.Allocated
}

func (t *treeMeter) fail(path string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.errs = append(t.errs, fmt.Errorf("%s: %w", path, err))
}

// measureTree walks a directory. rootMTime is used when the tree is empty so
// LastModified never stays unset.
func measureTree(ctx context.Context, path string, rootMTime time.Time) (measurement, error) {
	return measureWith(ctx, &treeMeter{links: map[string]struct{}{}}, path, rootMTime)
}

// measureWorktree is measureTree for a linked worktree directory: its own
// top-level .git link file is not reported as a nested repository, everything
// deeper is.
func measureWorktree(ctx context.Context, path string) (measurement, error) {
	root, err := walk.Stat(path)
	if err != nil {
		return measurement{}, err
	}
	t := &treeMeter{links: map[string]struct{}{}, ownGit: ".git"}
	return measureWith(ctx, t, path, root.ModTime)
}

func measureWith(ctx context.Context, t *treeMeter, path string, rootMTime time.Time) (measurement, error) {
	err := walk.Walk(ctx, path, walk.Options{Fresh: true}, t.visit, t.fail)
	if err != nil {
		return measurement{}, err
	}
	if len(t.errs) > 0 {
		return measurement{}, fmt.Errorf("cannot inspect the whole directory, so a nested git repository cannot be ruled out: %w", t.errs[0])
	}
	if t.m.newest.IsZero() {
		t.m.newest = rootMTime
	}
	return t.m, nil
}

// refreshFinding returns a copy of f describing the path as it is now: the
// resolved path, the freshly measured size and the newest mtime. It fails
// with a skip when the path is gone or holds a git repository.
func refreshFinding(ctx context.Context, f findings.Finding, path string) (findings.Finding, error) {
	m, err := sizeAndNestedGit(ctx, path)
	switch {
	case isGone(err):
		return f, skipf("already gone")
	case err != nil:
		return f, skipf("cannot inspect %s: %v", path, err)
	case m.nestedGit != "":
		return f, skipf("contains a git repository (.git at %s)", m.nestedGit)
	}
	f.Path = path
	f.SizeBytes = m.size
	mt := m.newest
	f.LastModified = &mt
	return f, nil
}
