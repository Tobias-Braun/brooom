// Package mergedbranch implements the merged-branch detector: local branches
// whose work is already contained in the base branch, the safest and most
// common clutter parallel agent work leaves behind.
//
// A branch counts as merged when its tip is an ancestor of the base
// (gitx.MethodAncestor) or, in mode "ancestor+squash", when its changes were
// squash- or rebase-merged (detected by patch-id in gitx). Merged detection is
// deliberately not gated by thresholds.min_age_days: a branch merged
// yesterday is exactly what `brooom branches --merged` is for. Recency is
// only reported through the informational recently_modified flag.
//
// Findings use KindBranch with Ref = branch name and Path = the MAIN
// worktree of the repository, so the same branch reached through several
// worktrees or targets yields one identical ID that the engine deduplicates.
//
// Meta keys:
//
//	tip           full sha of the branch tip (the delete action verifies it)
//	base          base ref the branch was compared with, e.g. origin/main
//	merge_method  ancestor | squash | rebase
//	upstream      configured upstream of the branch, when set
//	open_pr_check ok | unknown | disabled (state of the open PR lookup)
//	remote        "true" on findings for remote-tracking branches
//
// Evidence codes: merged_into, squash_merged_into, last_commit_age,
// current_branch_worktree, open_pr_unknown.
//
// Risk flags are always set honestly. A blocking flag downgrades the suggested
// action to none; under --force only force-overridable flags (has_open_pr) are
// overridden, and the suggestion then carries a "forced:" reason. Protected
// and checked-out branches are never overridden.
package mergedbranch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// Name is the detector name used in findings, config keys and --detector.
const Name = config.DetectorMergedBranch

// Detector reports local branches that are already merged into the base.
type Detector struct {
	// GH overrides the gh runner used for the open PR lookup. It is nil in
	// production (the real gh CLI is used) and set by tests.
	GH gitx.GHRunner
}

// New returns the detector.
func New() *Detector { return &Detector{} }

func init() { detect.Register(New()) }

// Name implements detect.Detector.
func (*Detector) Name() string { return Name }

// Description implements detect.Detector.
func (*Detector) Description() string {
	return "Local branches that are already merged into the base branch"
}

// Category implements detect.Detector.
func (*Detector) Category() detect.Category { return detect.CategoryGit }

// scan holds everything that is loaded once per target so the per-branch
// helpers stay small.
type scan struct {
	env      *detect.Env
	target   scope.Target
	cfg      *config.Config
	repo     *gitx.Repo
	base     gitx.Base
	baseTip  string
	path     string
	squash   bool
	prs      gitx.PRInfo
	prCheck  string
	branches []gitx.Branch
	emit     func(findings.Finding)
}

const (
	prCheckOK       = "ok"
	prCheckUnknown  = "unknown"
	prCheckDisabled = "disabled"
)

// Detect implements detect.Detector.
func (d *Detector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	if target.Kind != scope.TargetRepo {
		return nil
	}
	s, err := d.load(ctx, env, target, emit)
	if err != nil || s == nil {
		return err
	}
	for _, b := range s.branches {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.localBranch(ctx, b); err != nil {
			return err
		}
	}
	if s.cfg.Detectors.MergedBranch.IncludeRemote {
		return s.remoteBranches(ctx)
	}
	return nil
}

// load gathers the per-target state. It returns a nil scan without error when
// the repository has no base branch, since nothing can be merged into it.
func (d *Detector) load(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) (*scan, error) {
	cfg, err := env.Config.ForTarget(target.Scope.Path, target.Path)
	if err != nil {
		return nil, fmt.Errorf("merged-branch: config for %q: %w", target.Path, err)
	}
	repo, err := env.Repo(ctx, target.Path)
	if err != nil {
		return nil, fmt.Errorf("merged-branch: open %q: %w", target.Path, err)
	}
	main, err := repo.MainWorktree(ctx)
	if err != nil {
		return nil, fmt.Errorf("merged-branch: main worktree of %q: %w", target.Path, err)
	}
	path, err := env.Guard.Resolve(main)
	if err != nil {
		return nil, fmt.Errorf("merged-branch: main worktree %q of target %q is outside the allowed scope: %w", main, target.Path, err)
	}
	base, err := repo.DefaultBase(ctx, cfg.Git.BaseBranches)
	if errors.Is(err, gitx.ErrNoBase) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("merged-branch: base of %q: %w", target.Path, err)
	}
	branches, err := repo.ListBranches(ctx)
	if err != nil {
		return nil, fmt.Errorf("merged-branch: list branches of %q: %w", target.Path, err)
	}
	branches = slices.Clone(branches)
	sort.Slice(branches, func(i, j int) bool { return branches[i].Name < branches[j].Name })
	s := &scan{
		env: env, target: target, cfg: cfg, repo: repo, base: base, path: path,
		squash:   cfg.Detectors.MergedBranch.Mode == config.MergeAncestorSquash,
		prCheck:  prCheckDisabled,
		branches: branches,
		emit:     emit,
	}
	s.baseTip = s.tipOf(ctx)
	if cfg.Git.UseGH {
		s.prs = repo.OpenPRBranches(ctx, repo.Dir, gitx.PROptions{GH: d.GH})
		s.prCheck = prCheckUnknown
		if s.prs.Known {
			s.prCheck = prCheckOK
		}
	}
	return s, nil
}

// tipOf finds the tip sha of the base ref among the already listed refs, so
// no extra git call is needed. It returns "" when the ref is not found.
func (s *scan) tipOf(ctx context.Context) string {
	if s.base.Remote != "" {
		remotes, err := s.repo.ListRemoteBranches(ctx)
		if err != nil {
			return ""
		}
		for _, rb := range remotes {
			if rb.Name == s.base.Ref {
				return rb.Tip
			}
		}
		return ""
	}
	for _, b := range s.branches {
		if b.Name == s.base.Ref {
			return b.Tip
		}
	}
	return ""
}

// localBranch classifies one local branch and emits a finding when merged.
func (s *scan) localBranch(ctx context.Context, b gitx.Branch) error {
	if gitx.IsBaseBranch(s.base, s.cfg.Git.BaseBranches, b.Name) {
		return nil
	}
	if s.unstarted(ctx, b) {
		return nil
	}
	res, err := s.repo.MergedInto(ctx, s.base.Ref, b.Name, s.squash)
	if err != nil {
		// One unclassifiable branch must not hide the others.
		return ctx.Err()
	}
	if !res.Merged {
		return nil
	}
	s.emit(s.buildFinding(ctx, b, res.Method))
	return nil
}

// unstarted reports a freshly created branch that was never pushed and still
// sits on the base tip: not clutter, deleting it would only annoy.
func (s *scan) unstarted(ctx context.Context, b gitx.Branch) bool {
	if s.baseTip == "" || b.Tip != s.baseTip {
		return false
	}
	never, err := s.repo.NeverPushed(ctx, b)
	return err == nil && never
}

func (s *scan) newFinding(ref, tip string, date time.Time) findings.Finding {
	f := findings.Finding{
		ID:         findings.NewID(Name, findings.KindBranch, s.path, ref),
		Detector:   Name,
		Scope:      s.target.Scope,
		Path:       s.path,
		Kind:       findings.KindBranch,
		Ref:        ref,
		AgeDays:    s.env.AgeDays(date),
		Confidence: findings.ConfidenceHigh,
		Meta:       map[string]string{"tip": tip, "base": s.base.Ref},
		RiskFlags:  []findings.RiskFlag{},
	}
	if !date.IsZero() {
		t := date
		f.LastModified = &t
	}
	return f
}

// buildFinding assembles the complete finding of a merged local branch.
func (s *scan) buildFinding(ctx context.Context, b gitx.Branch, method string) findings.Finding {
	f := s.newFinding(b.Name, b.Tip, b.Date)
	f.Meta["merge_method"] = method
	f.Meta["open_pr_check"] = s.prCheck
	if b.Upstream != "" {
		f.Meta["upstream"] = b.Upstream
	}
	f.Evidence = s.evidence(b, method)
	f.RiskFlags = s.riskFlags(ctx, b)
	f.SuggestedAction = s.action(b.Name, method, f.RiskFlags)
	return f
}

func (s *scan) evidence(b gitx.Branch, method string) []findings.Evidence {
	ev := []findings.Evidence{mergedEvidence(method, s.base.Ref, "Branch")}
	days := s.env.AgeDays(b.Date)
	ev = append(ev, findings.Evidence{
		Code: "last_commit_age", Message: "Last commit is " + strconv.Itoa(days) + " days old.", Value: days,
	})
	if b.WorktreePath != "" {
		ev = append(ev, findings.Evidence{
			Code: "current_branch_worktree", Message: "Checked out in " + b.WorktreePath + ".", Value: b.WorktreePath,
		})
	}
	if s.prCheck == prCheckUnknown {
		ev = append(ev, findings.Evidence{
			Code:    "open_pr_unknown",
			Message: "Open pull requests could not be checked (gh unavailable).",
		})
	}
	return ev
}

// mergedEvidence builds the merged_into or squash_merged_into evidence; subject
// is "Branch" or "Remote branch".
func mergedEvidence(method, base, subject string) findings.Evidence {
	if method == gitx.MethodAncestor {
		return findings.Evidence{
			Code: "merged_into", Message: subject + " tip is contained in " + base + ".", Value: base,
		}
	}
	return findings.Evidence{
		Code: "squash_merged_into", Message: subject + " changes were " + methodWord(method) + " into " + base + ".", Value: base,
	}
}

// methodWord returns the past-tense wording for a non-ancestor merge.
func methodWord(method string) string {
	if method == gitx.MethodRebase {
		return "rebase-merged"
	}
	return "squash-merged"
}

// riskFlags returns the flags in a stable order: blocking flags first, then
// the informational ones.
func (s *scan) riskFlags(ctx context.Context, b gitx.Branch) []findings.RiskFlag {
	flags := []findings.RiskFlag{}
	if b.WorktreePath != "" {
		flags = append(flags, findings.RiskCurrentBranch)
	}
	if gitx.IsProtected(s.cfg.Git.ProtectedBranches, b.Name) {
		flags = append(flags, findings.RiskProtectedBranch)
	}
	if s.cfg.Git.UseGH && s.prs.HasOpenPR(b.Name) {
		flags = append(flags, findings.RiskHasOpenPR)
	}
	if b.UpstreamGone {
		flags = append(flags, findings.RiskUpstreamGone)
	}
	if never, err := s.repo.NeverPushed(ctx, b); err == nil && never {
		flags = append(flags, findings.RiskNeverPushed)
	}
	if s.recent(b.Date) {
		flags = append(flags, findings.RiskRecentlyModified)
	}
	return flags
}

func (s *scan) recent(date time.Time) bool {
	if date.IsZero() || s.cfg.Thresholds.RecentDays <= 0 {
		return false
	}
	return s.env.Now.Sub(date) < time.Duration(s.cfg.Thresholds.RecentDays)*24*time.Hour
}

// action decides the suggestion for a merged branch given its risk flags.
func (s *scan) action(name, method string, flags []findings.RiskFlag) findings.SuggestedAction {
	blocking := blockingFlags(flags)
	if len(blocking) == 0 {
		return deleteAction(name, method, s.base.Ref)
	}
	if s.env.Force && findings.Actionable(flags, true) {
		a := deleteAction(name, method, s.base.Ref)
		a.Reason = "forced: overriding " + strings.Join(blocking, ", ") + "; " + a.Reason
		return a
	}
	return findings.SuggestedAction{Type: findings.ActionNone, Reason: blockedReason(flags)}
}

func blockingFlags(flags []findings.RiskFlag) []string {
	var out []string
	for _, f := range flags {
		if f.Blocking() {
			out = append(out, string(f))
		}
	}
	return out
}

// blockedReason explains every blocking flag and states which of them --force
// does not override.
func blockedReason(flags []findings.RiskFlag) string {
	var parts []string
	for _, f := range flags {
		switch f {
		case findings.RiskCurrentBranch:
			parts = append(parts, "checked out in a worktree (--force does not override this)")
		case findings.RiskProtectedBranch:
			parts = append(parts, "protected branch (--force does not override this)")
		case findings.RiskHasOpenPR:
			parts = append(parts, "has an open pull request (--force overrides this)")
		}
	}
	return "not suggested for deletion: " + strings.Join(parts, "; ")
}

func deleteAction(name, method, base string) findings.SuggestedAction {
	q := shellQuote(name)
	if method == gitx.MethodAncestor {
		return findings.SuggestedAction{
			Type:    findings.ActionDeleteBranch,
			Command: "git branch -d " + q,
			Reason:  "fully merged into " + base,
		}
	}
	return findings.SuggestedAction{
		Type:    findings.ActionDeleteBranch,
		Args:    map[string]string{"verified": "squash"},
		Command: "git branch -D " + q,
		Reason:  methodWord(method) + " into " + base + "; -D is required because git cannot see the squash merge",
	}
}

// shellQuote single-quotes s unless it only contains characters that are safe
// in a shell word. The command is informational and never run through a shell.
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._/-", r)
		if !ok {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// remoteBranches reports merged remote-tracking branches. Deleting remote
// branches is out of scope, so they never carry an action.
func (s *scan) remoteBranches(ctx context.Context) error {
	remotes, err := s.repo.ListRemoteBranches(ctx)
	if err != nil {
		return fmt.Errorf("merged-branch: list remote branches of %q: %w", s.target.Path, err)
	}
	remotes = slices.Clone(remotes)
	sort.Slice(remotes, func(i, j int) bool { return remotes[i].Name < remotes[j].Name })
	for _, rb := range remotes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.skipRemote(rb) {
			continue
		}
		res, err := s.repo.MergedInto(ctx, s.base.Ref, rb.Name, s.squash)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if res.Merged {
			s.emit(s.remoteFinding(rb, res.Method))
		}
	}
	return nil
}

// skipRemote excludes HEAD-like refs, the base itself and base-named or
// protected remote branches.
func (s *scan) skipRemote(rb gitx.RemoteBranch) bool {
	if rb.Name == s.base.Ref {
		return true
	}
	_, short, ok := strings.Cut(rb.Name, "/")
	if !ok || short == "HEAD" {
		return true
	}
	return gitx.IsBaseBranch(s.base, s.cfg.Git.BaseBranches, short) ||
		gitx.IsProtected(s.cfg.Git.ProtectedBranches, short)
}

func (s *scan) remoteFinding(rb gitx.RemoteBranch, method string) findings.Finding {
	f := s.newFinding(rb.Name, rb.Tip, rb.Date)
	f.Meta["remote"] = "true"
	f.Meta["merge_method"] = method
	f.Evidence = []findings.Evidence{
		mergedEvidence(method, s.base.Ref, "Remote branch"),
		{Code: "last_commit_age", Message: "Last commit is " + strconv.Itoa(f.AgeDays) + " days old.", Value: f.AgeDays},
	}
	f.SuggestedAction = findings.SuggestedAction{
		Type:   findings.ActionNone,
		Reason: "remote branch deletion is out of scope",
	}
	return f
}
