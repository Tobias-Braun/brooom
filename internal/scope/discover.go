package scope

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// DiscoverOptions controls workspace discovery.
type DiscoverOptions struct {
	// MaxDepth limits how deep below a root targets are looked for (0 =
	// DefaultMaxDepth). Children of the root have depth 1; a target at
	// depth MaxDepth is found, one deeper is not.
	MaxDepth int
	// Exclude lists glob patterns (see Excluded) of directories to skip,
	// matched against the path relative to the root.
	Exclude []string
	// DescendIntoRepos makes discovery continue below a found repository or
	// project folder to find nested repositories. Off by default because it
	// costs a full walk of every repository.
	DescendIntoRepos bool
	// SkipDirs are plain directory names never descended into, in addition
	// to HugeDirNames (from config Scan.SkipDirs).
	SkipDirs []string
	// Concurrency is the number of parallel directory readers (0 = number
	// of CPUs, from config Scan.Concurrency).
	Concurrency int
	// OnError, when set, is called for directories that could not be read.
	// It may be called concurrently. Discovery continues regardless.
	OnError func(path string, err error)
}

// Discover walks each root and returns every git repository and every
// non-git project folder inside it as targets, with Scope set to the
// (resolved) root.
//
// Repositories are directories holding a .git directory or a .git file whose
// first line starts with "gitdir:" (linked worktrees, submodules); each is
// its own target. Project folders are directories without .git that hold a
// project marker (see HasProjectMarker). Both are leaves: discovery does not
// descend into them unless opts.DescendIntoRepos is set, and then only
// nested repositories are reported (a project folder inside a repository or
// another project is never a separate target). Directories in HugeDirNames,
// Library and AppData directly below the user's home, opts.SkipDirs and
// opts.Exclude matches are pruned, and symlinks are never followed. A root
// that is itself a repository is reported as a repo target; a root is never
// a project folder.
//
// Roots are made absolute and symlink-resolved. Duplicate roots (detected
// with os.SameFile) are walked once; when roots are nested, every target is
// reported once with the innermost root as its scope. The result is sorted
// by Path, then Kind, and identical across runs.
//
// Partial results: a root that is missing or not a directory is skipped and
// reported in the returned error (errors.Join, naming each root) while the
// targets of the other roots are still returned, so callers must use the
// targets even when err is non-nil. Unreadable directories go to
// opts.OnError and never fail discovery. When ctx is cancelled Discover
// returns nil and ctx.Err().
func Discover(ctx context.Context, roots []string, opts DiscoverOptions) ([]Target, error) {
	resolved, errs := resolveRoots(roots)
	var cands []candidate
	for _, root := range resolved {
		ts, err := discoverRoot(ctx, root, opts)
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, t := range ts {
			cands = append(cands, candidate{t: t, rootLen: len(root)})
		}
	}
	targets := dedupeTargets(cands)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Path != targets[j].Path {
			return targets[i].Path < targets[j].Path
		}
		return targets[i].Kind < targets[j].Kind
	})
	return targets, errors.Join(errs...)
}

// resolveRoots makes every root absolute and symlink-resolved and drops
// duplicates (same directory under another spelling, symlink or case). Bad
// roots are returned as errors naming the root.
func resolveRoots(roots []string) ([]string, []error) {
	var out []string
	var infos []os.FileInfo
	var errs []error
	for _, r := range roots {
		p, fi, err := resolveRoot(r)
		if err != nil {
			errs = append(errs, fmt.Errorf("scope: root %s: %w", r, err))
			continue
		}
		dup := false
		for _, other := range infos {
			if os.SameFile(fi, other) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, p)
			infos = append(infos, fi)
		}
	}
	return out, errs
}

// resolveRoot resolves one root and verifies it is a directory.
func resolveRoot(root string) (string, os.FileInfo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	p, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", nil, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", nil, err
	}
	if !fi.IsDir() {
		return "", nil, errors.New("not a directory")
	}
	return p, fi, nil
}

// candidate is a target together with the length of the root it was found
// under; a longer resolved root is the more inner one.
type candidate struct {
	t       Target
	rootLen int
}

// dedupeTargets collapses targets reported by several (nested) roots into
// one, keeping the innermost root as scope. Targets are bucketed by
// lowercase base name so os.SameFile only runs between plausible twins.
func dedupeTargets(cands []candidate) []Target {
	buckets := make(map[string][]int)
	var kept []candidate
	for _, c := range cands {
		key := strings.ToLower(filepath.Base(c.t.Path))
		idx := findTwin(kept, buckets[key], c.t)
		if idx < 0 {
			buckets[key] = append(buckets[key], len(kept))
			kept = append(kept, c)
			continue
		}
		if c.rootLen > kept[idx].rootLen {
			kept[idx] = c
		}
	}
	out := make([]Target, len(kept))
	for i, c := range kept {
		out[i] = c.t
	}
	return out
}

// findTwin returns the index in kept (limited to the bucket) of the target
// that is the same directory as t, or -1.
func findTwin(kept []candidate, bucket []int, t Target) int {
	if len(bucket) == 0 {
		return -1
	}
	fi, err := os.Lstat(t.Path)
	for _, i := range bucket {
		other := kept[i].t
		if other.Kind != t.Kind {
			continue
		}
		if other.Path == t.Path {
			return i
		}
		if err != nil {
			continue
		}
		if ofi, oerr := os.Lstat(other.Path); oerr == nil && os.SameFile(fi, ofi) {
			return i
		}
	}
	return -1
}

// finder collects the facts one walk learns. visit runs concurrently, so
// everything it writes is guarded by mu; decisions that depend on siblings
// or ancestors are made after the walk, which keeps results independent of
// scheduling.
type finder struct {
	opts     DiscoverOptions
	maxDepth int
	skip     []string
	homeInfo os.FileInfo

	mu      sync.Mutex
	repos   map[string]bool
	markers map[string][]string
}

// discoverRoot walks one resolved root and returns its targets.
func discoverRoot(ctx context.Context, root string, opts DiscoverOptions) ([]Target, error) {
	f := &finder{
		opts:     opts,
		maxDepth: opts.MaxDepth,
		skip:     append(append([]string(nil), HugeDirNames...), opts.SkipDirs...),
		repos:    map[string]bool{},
		markers:  map[string][]string{},
	}
	if f.maxDepth <= 0 {
		f.maxDepth = DefaultMaxDepth
	}
	if home, err := os.UserHomeDir(); err == nil {
		f.homeInfo, _ = os.Stat(home)
	}
	if isGitDir(root) {
		f.repos[root] = true
		if !opts.DescendIntoRepos {
			return f.targets(root), nil
		}
	}
	// Targets are recognised by their child entries (.git, marker files),
	// which sit one level below the target, hence the + 1.
	wopts := walk.Options{Concurrency: opts.Concurrency, SkipNames: f.skip, MaxDepth: f.maxDepth + 1}
	if err := walk.Walk(ctx, root, wopts, f.visit, opts.OnError); err != nil {
		return nil, fmt.Errorf("scope: root %s: %w", root, err)
	}
	return f.targets(root), nil
}

// visit is the walk callback: it prunes directories, notes repositories as
// soon as they are seen (so the walk never enters them by default) and
// collects marker files.
func (f *finder) visit(e walk.Entry) walk.Decision {
	if !e.IsDir() {
		f.noteFile(e)
		return walk.Continue
	}
	// Directories at maxDepth+1 exist only so their marker files could be
	// seen by their parent; they are never targets themselves.
	if e.Depth > f.maxDepth || f.pruned(e) {
		return walk.SkipDir
	}
	if isGitDir(e.Path) {
		f.mu.Lock()
		f.repos[e.Path] = true
		f.mu.Unlock()
		if !f.opts.DescendIntoRepos {
			return walk.SkipDir
		}
	}
	return walk.Continue
}

// noteFile records a project marker file against its parent directory. The
// root itself is never a project (a stray package.json in a workspace root
// must not swallow all its repositories), so depth-1 files are ignored.
func (f *finder) noteFile(e walk.Entry) {
	if e.Depth < 2 || !IsProjectMarker(e.Name) {
		return
	}
	parent := filepath.Dir(e.Path)
	f.mu.Lock()
	f.markers[parent] = append(f.markers[parent], e.Name)
	f.mu.Unlock()
}

// pruned reports whether a directory is never descended into: huge and
// configured names, home-only names, and exclude patterns.
func (f *finder) pruned(e walk.Entry) bool {
	for _, n := range f.skip {
		if sameName(n, e.Name) {
			return true
		}
	}
	return Excluded(f.opts.Exclude, e.Rel) || f.isHomeChild(e)
}

// isHomeChild reports whether e is Library or AppData directly below the
// user's home, comparing the parent with os.SameFile.
func (f *finder) isHomeChild(e walk.Entry) bool {
	if f.homeInfo == nil {
		return false
	}
	match := false
	for _, n := range homeOnlySkipNames {
		match = match || sameName(n, e.Name)
	}
	if !match {
		return false
	}
	pi, err := os.Stat(filepath.Dir(e.Path))
	return err == nil && os.SameFile(pi, f.homeInfo)
}

// targets turns the collected facts into targets. A target below another
// target is dropped when it is a project, or when it is a repository and
// DescendIntoRepos is off (it can then only have been seen inside a project
// folder). Deciding here instead of in visit keeps the result independent
// of which directories the parallel walk happened to enter.
func (f *finder) targets(root string) []Target {
	kinds := map[string]TargetKind{}
	for p := range f.repos {
		kinds[p] = TargetRepo
	}
	for p, names := range f.markers {
		if !f.repos[p] && HasProjectMarker(names) {
			kinds[p] = TargetProject
		}
	}
	sc := findings.Scope{Type: findings.ScopeRoot, Path: root}
	var out []Target
	for p, k := range kinds {
		if k == TargetProject || !f.opts.DescendIntoRepos {
			if hasAncestorTarget(kinds, p, root) {
				continue
			}
		}
		out = append(out, Target{Kind: k, Path: p, Scope: sc})
	}
	return out
}

// hasAncestorTarget reports whether a directory between p (exclusive) and
// root (inclusive) is itself a target.
func hasAncestorTarget(kinds map[string]TargetKind, p, root string) bool {
	for dir := filepath.Dir(p); len(dir) >= len(root); dir = filepath.Dir(dir) {
		if _, ok := kinds[dir]; ok {
			return true
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return false
}

// isGitDir reports whether dir holds a .git directory, or a .git regular
// file whose first line starts with "gitdir:". Symlinked .git entries do not
// count and other files named .git are ignored.
func isGitDir(dir string) bool {
	p := filepath.Join(dir, ".git")
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	if fi.IsDir() {
		return true
	}
	if !fi.Mode().IsRegular() {
		return false
	}
	fh, err := os.Open(p)
	if err != nil {
		return false
	}
	defer fh.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(fh, buf)
	line, _, _ := bytes.Cut(buf[:n], []byte("\n"))
	return bytes.HasPrefix(line, []byte("gitdir:"))
}
