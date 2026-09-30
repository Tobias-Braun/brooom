// Package buildartifacts implements the build-artifacts detector:
// dependency and build output directories (node_modules, target, .venv,
// .next, dist, ...) located through the embedded catalog and weighted by how
// long the surrounding project has been untouched.
//
// Confidence follows project inactivity, never the age of the artifact
// itself: a fresh node_modules of a project nobody has touched for months is
// still an easy win, while a stale-looking dist next to a file edited
// yesterday is not. Directories that git tracks are blocking findings, and a
// name such as dist only counts next to a project marker, because false
// positives destroy trust. The package also exports the claim matcher
// (Claims) so other detectors can skip what this catalog owns.
package buildartifacts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// Name is the detector name used in findings and config keys.
const Name = config.DetectorBuildArtifacts

func init() { detect.Register(New()) }

// Detector reports build artifact directories. It never modifies anything.
type Detector struct{}

// New returns the detector.
func New() *Detector { return &Detector{} }

// Name implements detect.Detector.
func (*Detector) Name() string { return Name }

// Description implements detect.Detector.
func (*Detector) Description() string {
	return "dependency and build output directories, weighted by project inactivity"
}

// Category implements detect.Detector.
func (*Detector) Category() detect.Category { return detect.CategoryArtifacts }

// candidate is a directory the matcher claimed, before any sizing or git
// work.
type candidate struct {
	rel  string
	m    match
	link bool
	// linkModTime is the modification time of a symlinked artifact (the
	// link itself; the target is never inspected further).
	linkModTime time.Time
}

// scan carries the per-target state shared by all candidates.
type scan struct {
	env    *detect.Env
	cfg    *config.Config
	target scope.Target
	root   string
	m      *matcher
	acts   *activityCache
}

// Detect implements detect.Detector.
func (d *Detector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	if target.Kind == scope.TargetUser {
		return nil
	}
	s, err := newScan(env, target)
	if err != nil || s == nil {
		return err
	}
	cands, err := s.collect(ctx)
	if err != nil {
		return err
	}
	return s.assessAll(ctx, cands, emit)
}

// newScan resolves the effective configuration and the guarded root. It
// returns nil when the detector is disabled for this target.
func newScan(env *detect.Env, target scope.Target) (*scan, error) {
	cfg, err := env.Config.ForTarget(target.Path)
	if err != nil {
		return nil, fmt.Errorf("build-artifacts: config for %s: %w", target.Path, err)
	}
	if !cfg.DetectorEnabled(Name) {
		return nil, nil
	}
	root, err := env.Guard.Resolve(target.Path)
	if err != nil {
		return nil, fmt.Errorf("build-artifacts: %w", err)
	}
	m, err := newMatcher(root, cfg.Detectors.BuildArtifacts)
	if err != nil {
		return nil, err
	}
	s := &scan{env: env, cfg: cfg, target: target, root: root, m: m, acts: newActivityCache()}
	return s, nil
}

// walkOptions are shared by the candidate walk and the activity walk. The
// walker itself always skips .git; the configured skip dirs are added.
func (s *scan) walkOptions() walk.Options {
	return walk.Options{Concurrency: s.cfg.Scan.Concurrency, SkipNames: s.cfg.Scan.SkipDirs}
}

// collect walks the target once and returns the claimed directories sorted
// by path. The walk never descends into a claimed directory, an excluded
// directory or a nested repository.
func (s *scan) collect(ctx context.Context) ([]candidate, error) {
	var mu sync.Mutex
	var out []candidate
	err := walk.Walk(ctx, s.root, s.walkOptions(), func(e walk.Entry) walk.Decision {
		c, dec := s.visit(e, e.Rel)
		if c != nil {
			mu.Lock()
			out = append(out, *c)
			mu.Unlock()
		}
		return dec
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("build-artifacts: walk %s: %w", s.root, err)
	}
	slices.SortFunc(out, func(a, b candidate) int { return strings.Compare(a.rel, b.rel) })
	return out, nil
}

// visit decides what to do with one entry; rel is its path relative to the
// scan root. Only directories and symlinks can be candidates.
func (s *scan) visit(e walk.Entry, rel string) (*candidate, walk.Decision) {
	link := e.IsSymlink()
	if !e.IsDir() && !link {
		return nil, walk.Continue
	}
	if s.pruned(e.Name, rel) {
		return nil, walk.SkipDir
	}
	if link {
		return s.linkCandidate(e, rel), walk.Continue
	}
	if isNestedRepo(e.Path) {
		return nil, walk.SkipDir
	}
	if m, ok := s.m.match(rel, true); ok {
		return &candidate{rel: rel, m: m}, walk.SkipDir
	}
	return nil, walk.Continue
}

// pruned reports whether a directory is never looked at: configured skip
// names and the exclude globs of the repository.
func (s *scan) pruned(name, rel string) bool {
	if slices.ContainsFunc(s.cfg.Scan.SkipDirs, func(n string) bool { return sameName(n, name) }) {
		return true
	}
	return scope.Excluded(s.cfg.RepoExclude, rel)
}

func sameName(a, b string) bool {
	if foldCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// isNestedRepo reports whether dir has its own .git entry (a directory, or
// a file for linked worktrees and submodules). Nested repositories are
// other scopes: scanning them here would report findings under a repo this
// target does not own.
func isNestedRepo(dir string) bool {
	return walk.HasVCSEntry(dir)
}

// linkCandidate claims a symlink that points to a directory and matches a
// rule. Rules that inspect the inside of the directory are skipped: that
// would follow the link, and a symlinked virtual environment is not worth
// it.
func (s *scan) linkCandidate(e walk.Entry, rel string) *candidate {
	fi, err := os.Stat(e.Path)
	if err != nil || !fi.IsDir() {
		return nil
	}
	m, ok := s.m.match(rel, true)
	if !ok || m.rule.requireFile != "" {
		return nil
	}
	return &candidate{rel: rel, m: m, link: true, linkModTime: e.ModTime}
}

// resolve turns a candidate into the guarded absolute path. Symlinks keep
// their final element (ResolveParent), so only the link is ever addressed.
func (s *scan) resolve(c candidate) (string, error) {
	p := filepath.Join(s.root, filepath.FromSlash(c.rel))
	if c.link {
		return s.env.Guard.ResolveParent(p)
	}
	return s.env.Guard.Resolve(p)
}

// assessAll builds the findings with bounded parallelism (sizing and git
// calls dominate) and emits them sequentially in path order, so emit never
// runs concurrently and the output is deterministic.
func (s *scan) assessAll(ctx context.Context, cands []candidate, emit func(findings.Finding)) error {
	results := make([]*findings.Finding, len(cands))
	limit := s.cfg.Scan.Concurrency
	if limit <= 0 {
		limit = 4
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range cands {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = s.assess(ctx, cands[i])
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, f := range results {
		if f != nil {
			emit(*f)
		}
	}
	return nil
}
