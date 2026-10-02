package logs

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/catalog"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// maxProjectDepth bounds the project walk. Logs and caches live near the
// project root; a deeper walk would mostly cost time in vendored trees.
const maxProjectDepth = 10

// run carries everything one Detect call needs. It is created per call, so
// concurrent targets never share it.
type run struct {
	d      *Detector
	env    *detect.Env
	cfg    *config.Config
	cat    *catalog.Catalog
	target scope.Target
	// protected reports whether an absolute path is protected by the
	// catalog (project or user rules, depending on the target kind).
	protected func(abs string) bool
	names     map[string]string
}

// candidate is a path that matched a catalog entry, before it was sized,
// aged and checked.
type candidate struct {
	path    string
	isDir   bool
	symlink bool
	toolID  string
	// category is the catalog category of the matching tool, reported in
	// the finding's meta.
	category catalog.Category
	entry    catalog.Entry
	pattern  string
}

// candidates collects the matches of the target.
func (r *run) candidates(ctx context.Context) ([]candidate, error) {
	return r.projectCandidates(ctx)
}

// collector gathers candidates from the walk workers, which call the visit
// function concurrently.
type collector struct {
	mu  sync.Mutex
	out []candidate
}

func (c *collector) add(cand candidate) {
	c.mu.Lock()
	c.out = append(c.out, cand)
	c.mu.Unlock()
}

// projectCandidates does one pruned walk over the target and returns the
// entries matching the project patterns of the handled categories. Matched directories are not
// descended into (the whole directory is the finding), nested repositories
// and excluded directories are pruned.
func (r *run) projectCandidates(ctx context.Context) ([]candidate, error) {
	m := r.cat.ProjectMatcher(handled...)
	r.protected = func(abs string) bool {
		rel, err := filepath.Rel(r.target.Path, abs)
		return err == nil && m.Protected(filepath.ToSlash(rel))
	}
	fold := r.cat.GOOS() == "windows" || r.cat.GOOS() == "darwin"
	col := &collector{}
	opts := walk.Options{
		Concurrency: r.cfg.Scan.Concurrency,
		SkipNames:   append(slices.Clone(scope.ProjectSkipDirs), r.cfg.Scan.SkipDirs...),
		MaxDepth:    maxProjectDepth,
	}
	err := walk.Walk(ctx, r.target.Path, opts, func(e walk.Entry) walk.Decision {
		return r.visit(m, fold, col, e)
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("log-and-runtime-files: walk %s: %w", r.target.Path, err)
	}
	return col.out, nil
}

// visit decides for one walked entry. Pruning comes before matching so that
// nothing below an excluded directory or inside a nested repository is ever
// reported.
func (r *run) visit(m *catalog.ProjectMatcher, fold bool, col *collector, e walk.Entry) walk.Decision {
	if e.IsDir() {
		if r.excluded(e) || hasGitEntry(e.Path) {
			return walk.SkipDir
		}
		if c, ok := matchEntry(m, fold, e, true); ok {
			col.add(c)
			return walk.SkipDir
		}
		return walk.Continue
	}
	if c, ok := matchEntry(m, fold, e, false); ok {
		col.add(c)
	} else if e.IsSymlink() {
		// A symlink is one object whatever it points to; entries that only
		// match directories must still catch a link with that name.
		if c, ok := matchEntry(m, fold, e, true); ok {
			col.add(c)
		}
	}
	return walk.Continue
}

// matchEntry matches the project-relative path of e against the catalog.
func matchEntry(m *catalog.ProjectMatcher, fold bool, e walk.Entry, asDir bool) (candidate, bool) {
	match, ok := m.Match(e.Rel, asDir)
	if !ok {
		return candidate{}, false
	}
	return candidate{
		path:     e.Path,
		isDir:    asDir && e.IsDir(),
		symlink:  e.IsSymlink(),
		toolID:   match.ToolID,
		category: match.Category,
		entry:    match.Entry,
		pattern:  matchedPattern(match.Entry, e.Rel, fold),
	}, true
}

// matchedPattern names the pattern of the entry that matched rel, with the
// same semantics as catalog.ProjectMatcher (the matcher itself does not
// report which of the entry's patterns hit).
func matchedPattern(e catalog.Entry, rel string, fold bool) string {
	rel = path.Clean(filepath.ToSlash(rel))
	for _, p := range e.Patterns {
		target := rel
		if !strings.Contains(p, "/") {
			target = path.Base(rel)
		}
		if catalog.GlobMatch(p, target, fold) {
			return p
		}
	}
	return e.Patterns[0]
}

// excluded applies the exclude list of the repository's .brooom.json
// (relative to the target).
func (r *run) excluded(e walk.Entry) bool {
	return scope.Excluded(r.cfg.RepoExclude, e.Rel)
}

// hasGitEntry reports whether dir directly contains a .git entry, file or
// directory. Lstat keeps a symlinked .git from being followed.
func hasGitEntry(dir string) bool {
	return walk.HasVCSEntry(dir)
}

// toolName returns the display name of a catalog tool for evidence messages.
func (r *run) toolName(id string) string {
	if r.names == nil {
		r.names = map[string]string{}
		for _, t := range r.cat.Tools() {
			r.names[t.ID] = t.Name
		}
	}
	if n := r.names[id]; n != "" {
		return n
	}
	return id
}
