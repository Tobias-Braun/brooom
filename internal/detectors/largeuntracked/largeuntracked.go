// Package largeuntracked implements the large-untracked detector: big files
// that git does not track (datasets, model checkpoints, dumps, forgotten
// videos) and, optionally, big ignored files and directories inside a
// repository.
//
// The detector is read-only. It runs one `git ls-files` per mode through
// env.Git, inspects the candidates with Lstat (symlinks are never followed)
// and sizes ignored directories with a fresh walk.DirSize.
//
// # Safety model
//
// An untracked file that is not ignored may be the only copy of the user's
// work. It is therefore reported with low confidence, suggested only as
// `trash` (recoverable) and marked with Meta["user_data_risk"]="untracked" so
// that presets can exclude it from the safe set and the trash action can
// refuse the permanent `delete` strategy for it. Untracked files are
// deliberately NOT flagged uncommitted_changes: that blocking flag would turn
// the suggestion into `none` and defeat the purpose of the detector; the low
// confidence and the untracked_file evidence carry the warning instead.
// Ignored entries are more likely regenerable and get medium confidence and
// the gitignored flag.
//
// # Configuration
//
// detectors.large-untracked.min_size_bytes is the size threshold. A value of
// 0 (or a negative one) means the default of 100 MiB, so a mis-set config can
// never flood the report with every file. include_ignored controls the
// ignored listing.
//
// # Claims of other detectors
//
// Directories that the build-artifacts, ai-artifacts or
// log-and-runtime-files detectors report (and everything below them) are
// skipped, see claimSet. Build artifacts use the build-artifacts detector's own
// matcher (buildartifacts.ClaimsWith, markers included), so the two detectors
// never double-report a directory.
package largeuntracked

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// Name is the detector name used in findings and config keys.
const Name = "large-untracked"

// DefaultMinSizeBytes is the threshold used when the configured one is not
// positive.
const DefaultMinSizeBytes int64 = 100 << 20

// openFiles is a seam for tests, which cannot rely on real open handles.
var openFiles = procs.OpenFiles

func init() { detect.Register(New()) }

// Detector reports large untracked and ignored files.
type Detector struct{}

// New returns the large-untracked detector.
func New() *Detector { return &Detector{} }

// Name implements detect.Detector.
func (*Detector) Name() string { return Name }

// Description implements detect.Detector.
func (*Detector) Description() string {
	return "Large files git does not track, and large ignored files or directories, inside repositories"
}

// Category implements detect.Detector.
func (*Detector) Category() detect.Category { return detect.CategoryFiles }

// candidate is an entry that passed the cheap filters and was inspected.
type candidate struct {
	entry
	path string // resolved absolute path
	size int64
	mod  time.Time
}

// Detect implements detect.Detector.
func (d *Detector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	if target.Kind != scope.TargetRepo {
		return nil
	}
	cfg, err := env.Config.ForTarget(target.Scope.Path, target.Path)
	if err != nil {
		return err
	}
	if !cfg.Detectors.LargeUntracked.Enabled {
		return nil
	}
	cl, err := newClaims(target.Path, cfg)
	if err != nil {
		return err
	}
	s := &scan{env: env, target: target, cfg: cfg, min: minSize(cfg), claims: cl}
	entries, err := s.list(ctx)
	if err != nil {
		return err
	}
	cands, err := s.inspect(ctx, s.prefilter(entries))
	if err != nil {
		return err
	}
	open, openErr := s.checkOpen(ctx, cands)
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, c := range cands {
		emit(s.finding(c, open[c.path], openErr != nil))
	}
	return nil
}

// minSize applies the documented default for non-positive thresholds.
func minSize(cfg *config.Config) int64 {
	if m := cfg.Detectors.LargeUntracked.MinSizeBytes; m > 0 {
		return m
	}
	return DefaultMinSizeBytes
}

// scan holds the per-target state.
type scan struct {
	env    *detect.Env
	target scope.Target
	cfg    *config.Config
	min    int64
	claims claimSet
}

// list returns the untracked and, when configured, the ignored entries.
func (s *scan) list(ctx context.Context) ([]entry, error) {
	entries, err := listOthers(ctx, s.env.Git, s.target.Path, false)
	if err != nil {
		return nil, err
	}
	if s.cfg.Detectors.LargeUntracked.IncludeIgnored {
		ign, err := listOthers(ctx, s.env.Git, s.target.Path, true)
		if err != nil {
			return nil, err
		}
		entries = append(entries, ign...)
	}
	return entries, nil
}

// prefilter drops, before any stat or sizing, nested repositories and linked
// worktrees (untracked "dir/" entries), excluded paths and paths claimed by
// other detectors.
func (s *scan) prefilter(entries []entry) []entry {
	var out []entry
	for _, e := range entries {
		if e.dir && !e.ignored {
			continue
		}
		if s.excluded(e) || s.claims.Covers(e.rel, e.dir) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// excluded applies the root and repo exclude globs. scope.Excluded matches
// the base name of the path it is given, so every ancestor prefix is tested
// too: excluding "data" must also hide data/big.bin.
func (s *scan) excluded(e entry) bool {
	rootRel := ""
	if s.cfg.RootPath != "" {
		abs := e.abs(s.target.Path)
		if r, err := filepath.Rel(s.cfg.RootPath, abs); err == nil && safeRel(filepath.ToSlash(r)) {
			rootRel = filepath.ToSlash(r)
		}
	}
	return anyPrefixExcluded(s.cfg.RootExclude, rootRel) || anyPrefixExcluded(s.cfg.RepoExclude, e.rel)
}

func anyPrefixExcluded(patterns []string, rel string) bool {
	if len(patterns) == 0 || rel == "" {
		return false
	}
	segs := strings.Split(rel, "/")
	for i := range segs {
		if scope.Excluded(patterns, strings.Join(segs[:i+1], "/")) {
			return true
		}
	}
	return false
}

// inspect stats and sizes the entries in bounded parallel and keeps those at
// or above the threshold, in path order. Failures on single entries (vanished
// files, unreadable directories, paths the guard refuses) skip the entry.
func (s *scan) inspect(ctx context.Context, entries []entry) ([]candidate, error) {
	res := make([]*candidate, len(entries))
	sem := make(chan struct{}, max(2, runtime.NumCPU()))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() == nil {
				res[i] = s.inspectOne(ctx, e)
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []candidate
	for _, c := range res {
		if c != nil {
			out = append(out, *c)
		}
	}
	slices.SortFunc(out, func(a, b candidate) int { return strings.Compare(a.path, b.path) })
	return out, nil
}

// inspectOne returns the candidate for e, or nil when it must not be reported.
func (s *scan) inspectOne(ctx context.Context, e entry) *candidate {
	abs := e.abs(s.target.Path)
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil
	}
	c := &candidate{entry: e}
	switch {
	case e.dir:
		if !fi.IsDir() || hasGitEntry(abs) {
			return nil
		}
		// Fresh, so a cached NewestModTime cannot hide a recent change.
		sum, err := walk.DirSize(ctx, abs, walk.Options{CacheDir: s.env.CacheDir, Fresh: true})
		if err != nil {
			return nil
		}
		c.size, c.mod = sum.SizeBytes, sum.NewestModTime
	case fi.Mode().IsRegular():
		c.size, c.mod = fi.Size(), fi.ModTime()
	default:
		// Symlinks, sockets, devices and FIFOs are never candidates.
		return nil
	}
	if c.size < s.min {
		return nil
	}
	resolved, err := s.env.Guard.Resolve(abs)
	if err != nil {
		return nil
	}
	c.path = resolved
	return c
}

// hasGitEntry reports whether dir directly contains a .git file or directory,
// i.e. is a nested repository, submodule or linked worktree.
func hasGitEntry(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// checkOpen looks up all candidates in one batch. The returned error is
// non-nil when the answer is incomplete or unavailable; true entries in the
// map are reliable regardless.
func (s *scan) checkOpen(ctx context.Context, cands []candidate) (map[string]bool, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	paths := make([]string, len(cands))
	for i, c := range cands {
		paths[i] = c.path
	}
	res, err := openFiles(ctx, paths)
	if err != nil && !errors.Is(err, procs.ErrUnavailable) && !errors.Is(err, procs.ErrIncomplete) {
		// Any failure means unknown, never safe, and must not fail the scan.
		err = fmt.Errorf("%w: %w", procs.ErrUnavailable, err)
	}
	return res, err
}
