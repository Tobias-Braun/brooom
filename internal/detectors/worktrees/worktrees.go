// Package worktrees implements the worktrees detector: linked git worktrees
// that parallel agents left behind. It reports worktrees whose branch is
// merged, whose upstream branch was deleted, whose directory is gone
// (prunable metadata that also blocks deleting the branch) and, when enabled,
// abandoned checkouts that have not been touched for a long time.
//
// The detector is read-only. It uses `git worktree list --porcelain`,
// `git status` with optional locks disabled and a size walk, all through the
// helpers of internal/gitx and internal/walk. The main worktree and bare
// entries are never reported, and neither is the worktree that is the scan
// scope itself, so `brooom worktrees` run inside a linked worktree can never
// propose removing the directory the user stands in.
//
// # Confidence and blocking
//
// Merged branches are high confidence; a detached HEAD contained in the base
// or a remote, a gone upstream whose commits all exist on a remote, and stale
// checkouts are medium. Dirty worktrees are reported with the blocking
// worktree_dirty flag and no suggested action; only --force (env.Force)
// turns that into a remove-worktree suggestion, which the action moves to the
// trash instead of deleting. Locked worktrees are never suggested, also not
// with --force.
//
// # Active worktrees
//
// A worktree an agent is working in must never look removable. The newest
// modification time comes from a Fresh walk.DirSize (the cache would miss
// in-place writes). A candidate modified within thresholds.recent_days gets
// the informational recently_modified flag and one lower confidence level
// (high to medium), which keeps it out of the safe preset. A worktree that
// contains the working directory of this process, or that a process has open
// or stands in (procs, best effort per OS), gets the blocking
// file_open_by_process flag and no action, also with --force. An unavailable
// open-file check is unknown, not safe: it is noted as evidence and the
// action re-checks at apply time.
package worktrees

import (
	"context"
	"errors"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// Name is the detector name used in findings and config keys.
const Name = "worktrees"

func init() { detect.Register(New()) }

// Detector reports leftover linked worktrees.
type Detector struct{}

// New returns the worktrees detector.
func New() *Detector { return &Detector{} }

// Name implements detect.Detector.
func (*Detector) Name() string { return Name }

// Description implements detect.Detector.
func (*Detector) Description() string {
	return "Leftover git worktrees: merged, upstream gone, directory missing or abandoned"
}

// Category implements detect.Detector.
func (*Detector) Category() detect.Category { return detect.CategoryGit }

// Detect implements detect.Detector.
func (d *Detector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	if target.Kind != scope.TargetRepo {
		return nil
	}
	s, err := newScan(ctx, env, target)
	if err != nil || s == nil {
		return err
	}
	wts, err := s.repo.ListWorktrees(ctx)
	if err != nil {
		return err
	}
	var notes []error
	for _, wt := range wts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.visit(ctx, wt, emit); err != nil {
			var skip *outOfScopeError
			if !errors.As(err, &skip) {
				return err
			}
			notes = append(notes, err)
		}
	}
	return errors.Join(notes...)
}

// outOfScopeError is the non-fatal note for a linked worktree that the guard
// does not allow. It is returned after every other worktree was examined, so
// it ends up as a scan error (shown with --verbose) and never hides findings.
type outOfScopeError struct{ path string }

func (e *outOfScopeError) Error() string {
	return "linked worktree " + e.path + " skipped: " + scope.OutsideWorktreeHint
}

// visit examines one worktree entry and emits its finding, if any. The main
// worktree and bare entries are never reported.
func (s *scan) visit(ctx context.Context, wt gitx.Worktree, emit func(findings.Finding)) error {
	if wt.Main || wt.Bare {
		return nil
	}
	e, ok := s.entry(wt)
	if !ok {
		return s.skipNote(wt)
	}
	f, ok, err := s.examine(ctx, e)
	if err != nil || !ok {
		return err
	}
	emit(f)
	return nil
}

// scan holds the per-target state shared by the worktrees of one repository.
type scan struct {
	env    *detect.Env
	target scope.Target
	cfg    *config.Config
	repo   *gitx.Repo
	// main is the path git reports for the main worktree; actions run git
	// there because a finding's Path is the linked worktree.
	main     string
	base     gitx.Base
	hasBase  bool
	squash   bool
	branches map[string]gitx.Branch
}

// newScan prepares the shared state. It returns nil (and no error) when the
// detector is disabled for the target or the repository has no working
// directory (bare).
func newScan(ctx context.Context, env *detect.Env, target scope.Target) (*scan, error) {
	cfg, err := env.Config.ForTarget(target.Scope.Path, target.Path)
	if err != nil {
		return nil, err
	}
	if !cfg.Detectors.Worktrees.Enabled {
		return nil, nil
	}
	repo, err := env.Repo(ctx, target.Path)
	if err != nil {
		return nil, err
	}
	main, err := repo.MainWorktree(ctx)
	if errors.Is(err, gitx.ErrBareRepo) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := &scan{
		env: env, target: target, cfg: cfg, repo: repo, main: main,
		squash: cfg.Detectors.MergedBranch.Mode == config.MergeAncestorSquash,
	}
	// Without a resolvable base branch merge checks are skipped; the other
	// classifications still work, so this is not an error.
	if base, err := repo.DefaultBase(ctx, cfg.Git.BaseBranches); err == nil {
		s.base, s.hasBase = base, true
	}
	branches, err := repo.ListBranches(ctx)
	if err != nil {
		return nil, err
	}
	s.branches = make(map[string]gitx.Branch, len(branches))
	for _, b := range branches {
		s.branches[b.Name] = b
	}
	return s, nil
}

// entry resolves the worktree path through the guard and decides whether the
// worktree may be looked at. Worktrees outside the guard are skipped, and so
// is the worktree that is the scan scope.
func (s *scan) entry(wt gitx.Worktree) (*entry, bool) {
	e := &entry{wt: wt, missing: wt.DirMissing || wt.Prunable}
	path, err := s.env.Guard.Resolve(wt.Path)
	if err != nil {
		// Exception to skip-outside-guard: pruning only edits git metadata in
		// the repository's git directory and never touches the recorded path,
		// so a missing worktree is reported whenever its repository is inside
		// the guard.
		if !e.missing || !s.mainInGuard() {
			return nil, false
		}
		path = wt.Path
	}
	if gitx.SamePath(path, s.target.Scope.Path) {
		return nil, false
	}
	e.path = path
	return e, true
}

// skipNote explains a skipped entry: a worktree that exists but lies outside
// the guard gets a note, the scan scope itself and unreachable missing
// worktrees stay silent.
func (s *scan) skipNote(wt gitx.Worktree) error {
	if wt.DirMissing || wt.Prunable || gitx.SamePath(wt.Path, s.target.Scope.Path) {
		return nil
	}
	if s.env.Guard.OutsideNote(wt.Path) == "" {
		return nil
	}
	return &outOfScopeError{path: wt.Path}
}

func (s *scan) mainInGuard() bool {
	_, err := s.env.Guard.ResolveRepoMeta(s.main)
	return err == nil
}
