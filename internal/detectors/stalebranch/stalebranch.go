// Package stalebranch implements the stale-branch detector: local branches
// whose last commit is older than the configured age and that no merge
// detector claims. Its central rule is the safety principle: a branch with
// commits that exist on no remote holds work that would be lost, so it is
// reported but blocked, and only an explicit --force lifts the block.
package stalebranch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// Name is the detector name used in findings and config keys.
const Name = config.DetectorStaleBranch

// Values of the open_pr_check meta key.
const (
	prCheckOK       = "ok"
	prCheckUnknown  = "unknown"
	prCheckDisabled = "disabled"
)

func init() { detect.Register(New()) }

// Detector reports stale local branches. It never modifies the repository.
type Detector struct {
	// GH runs the gh CLI for the open pull request lookup; nil uses the gh
	// binary on PATH. It is a field so tests need no real gh.
	GH gitx.GHRunner
}

// New returns the detector with default dependencies.
func New() *Detector { return &Detector{} }

// Name implements detect.Detector.
func (*Detector) Name() string { return Name }

// Description implements detect.Detector.
func (*Detector) Description() string {
	return "local branches whose last commit is old and that were never merged"
}

// Category implements detect.Detector.
func (*Detector) Category() detect.Category { return detect.CategoryGit }

// scan carries the per-target state shared by all branches of one repository.
type scan struct {
	env   *detect.Env
	cfg   *config.Config
	repo  *gitx.Repo
	path  string
	scope findings.Scope
	base  gitx.Base
	// bases lists every candidate a merge counts against, base first.
	bases   []gitx.Base
	hasBase bool
	pr      gitx.PRInfo
	prCheck string
	// mergedFail aggregates failed merged checks: on a broken repository every
	// branch fails the same way, and one error per repository says it once.
	mergedFail mergedFailure
}

// Detect implements detect.Detector.
func (d *Detector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	if target.Kind != scope.TargetRepo {
		return nil
	}
	s, err := d.newScan(ctx, env, target)
	if err != nil {
		return err
	}
	branches, err := s.repo.ListBranches(ctx)
	if err != nil {
		return fmt.Errorf("stale-branch: list branches of %s: %w", target.Path, err)
	}
	// The listing is memoized and shared, so sort a copy for a deterministic
	// emission order.
	branches = slices.Clone(branches)
	slices.SortFunc(branches, func(a, b gitx.Branch) int { return strings.Compare(a.Name, b.Name) })
	// Per-branch git failures are collected and returned joined at the end:
	// one broken branch must not hide the others, but it must show up as a
	// scan error instead of silently missing from the report.
	var errs []error
	// A bounded worker pool assesses the branches (each costs several git
	// processes); results are consumed in branch order so findings, errors and
	// the "first failed branch" of the aggregate do not depend on scheduling.
	results := detect.MapOrdered(ctx, branches, detect.BranchWorkers, s.assess)
	// Results of workers that were running when ctx was cancelled may reflect
	// the cancellation (e.g. "not merged"); drop them all.
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, a := range results {
		if a.mergedErr != nil {
			s.mergedFail.record(a.branch, a.mergedErr)
		}
		if a.err != nil {
			errs = append(errs, a.err)
		}
		if a.ok {
			emit(a.finding)
		}
	}
	if err := s.mergedFail.error(s.base.Ref); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// newScan resolves configuration, repository, guarded main worktree path,
// base branch and pull request state for one target.
func (d *Detector) newScan(ctx context.Context, env *detect.Env, target scope.Target) (*scan, error) {
	hint := ""
	if target.Scope.Type == findings.ScopeRoot {
		hint = target.Scope.Path
	}
	cfg, err := env.Config.ForTarget(hint, target.Path)
	if err != nil {
		return nil, fmt.Errorf("stale-branch: config for %s: %w", target.Path, err)
	}
	repo, err := env.Repo(ctx, target.Path)
	if err != nil {
		return nil, fmt.Errorf("stale-branch: open %s: %w", target.Path, err)
	}
	main, err := repo.MainWorktree(ctx)
	if err != nil {
		return nil, fmt.Errorf("stale-branch: main worktree of %s: %w", target.Path, err)
	}
	// The main worktree path keeps finding IDs identical from every worktree
	// of one repository; a main worktree outside the guard is refused.
	path, err := env.Guard.ResolveRepoMeta(main)
	if err != nil {
		return nil, fmt.Errorf("stale-branch: %w", err)
	}
	s := &scan{env: env, cfg: cfg, repo: repo, path: path, scope: target.Scope, prCheck: prCheckDisabled}
	// A repository without a resolvable base is still scanned: nothing is
	// merged then, so only the merged check is skipped.
	switch bases, err := repo.BaseCandidates(ctx, cfg.Git.BaseBranches); {
	case err == nil:
		s.base, s.bases, s.hasBase = bases[0], bases, true
	case !errors.Is(err, gitx.ErrNoBase):
		return nil, fmt.Errorf("stale-branch: base branch of %s: %w", path, err)
	}
	if cfg.Git.UseGH {
		s.pr = repo.OpenPRBranches(ctx, main, gitx.PROptions{GH: d.GH})
		s.prCheck = prCheckUnknown
		if s.pr.Known {
			s.prCheck = prCheckOK
		}
	}
	return s, nil
}

// isCandidate applies the cheap filters: old enough, not a base branch and
// not protected. Protected branches are not reported at all, unlike in
// merged-branch, because a stale release branch is long-lived on purpose.
func (s *scan) isCandidate(b gitx.Branch) bool {
	if s.env.AgeDays(b.Date) < max(s.cfg.Detectors.StaleBranch.MinAgeDays, s.cfg.Thresholds.AgeFloor()) {
		return false
	}
	if gitx.IsBaseBranch(s.base, s.cfg.Git.BaseBranches, b.Name) {
		return false
	}
	return !gitx.IsProtected(s.cfg.Git.ProtectedBranches, b.Name)
}

// mergedFailure remembers the first failed merged check and how many branches
// failed, so Detect can return a single error for the repository.
type mergedFailure struct {
	count int
	first string
	err   error
}

// record notes one failed check.
func (m *mergedFailure) record(branch string, err error) {
	if m.count == 0 {
		m.first, m.err = branch, err
	}
	m.count++
}

// error renders the aggregate, or nil when no check failed. The first branch
// is named with its cause; the others only counted, since their cause is the
// same in practice.
func (m *mergedFailure) error(base string) error {
	if m.count == 0 {
		return nil
	}
	more := ""
	if m.count > 1 {
		more = fmt.Sprintf(" (and %d more branches)", m.count-1)
	}
	return fmt.Errorf("stale-branch: check whether branch %q is merged into %s failed%s: %w", m.first, base, more, m.err)
}

// mergedSkip reports whether merged-branch owns the branch. It uses the same
// MergedInto call and mode as that detector so the two never disagree. A
// failure means unknown, which is never treated as merged; it is returned so
// Detect can record it in s.mergedFail (in branch order) and surface it once
// per repository. It is safe for concurrent use.
func (s *scan) mergedSkip(ctx context.Context, name string) (skip bool, failure error) {
	if !s.hasBase {
		return false, nil
	}
	squash := s.cfg.Detectors.MergedBranch.Mode == config.MergeAncestorSquash
	_, res, err := s.repo.MergedIntoAny(ctx, s.bases, "refs/heads/"+name, squash)
	if err != nil {
		if ctx.Err() == nil {
			return false, err
		}
		return false, nil
	}
	return res.Merged, nil
}
