package worktrees

import (
	"context"
	"fmt"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// Evidence codes of this detector. The merge codes are shared with the
// branch detectors so output and actions treat them identically.
const (
	evMissing      = "worktree_missing"
	evMerged       = "merged_into"
	evSquashMerged = "squash_merged_into"
	evHeadIn       = "head_contained_in"
	evUpstreamGone = "upstream_gone"
	evStale        = "worktree_stale"
	evHeadUnpushed = "head_not_pushed"
	evDirty        = "worktree_dirty"
	evLocked       = "worktree_locked"
	evOperation    = "worktree_operation_in_progress"
	evSubmodules   = "worktree_has_submodules"
	evLocation     = "agent_worktree_location"
	evOutsideScope = "outside_scope"
)

// entry is one linked worktree under examination together with the lazily
// computed facts about it.
type entry struct {
	wt gitx.Worktree
	// path is the guard-resolved worktree directory.
	path    string
	missing bool
	// sum and sized hold the directory summary; sizeTried avoids walking a
	// directory twice when the stale test and the size both need it.
	sum       walk.DirSummary
	sized     bool
	sizeTried bool
	// headTime is the committer time of HEAD, zero when unknown.
	headTime  time.Time
	headTried bool
	// unpushedDetached marks a detached HEAD whose commits exist nowhere else.
	unpushedDetached bool
}

// verdict is the classification of a candidate worktree.
type verdict struct {
	conf     findings.Confidence
	action   findings.ActionType
	reason   string
	evidence []findings.Evidence
	risks    []findings.RiskFlag
}

// rule classifies an entry. ok is false when the rule does not apply.
type rule func(ctx context.Context, e *entry) (v verdict, ok bool, err error)

// examine classifies one worktree and, when it is a candidate, sizes it and
// applies the blocking flags. Non-candidates produce no finding.
func (s *scan) examine(ctx context.Context, e *entry) (findings.Finding, bool, error) {
	v, ok, err := s.classify(ctx, e)
	if err != nil || !ok {
		return findings.Finding{}, false, err
	}
	if !e.missing {
		if err := s.summarize(ctx, e); err != nil {
			return findings.Finding{}, false, err
		}
	}
	if err := s.flagBlocking(ctx, e, &v); err != nil {
		return findings.Finding{}, false, err
	}
	if err := s.markActivity(ctx, e, &v); err != nil {
		return findings.Finding{}, false, err
	}
	return s.build(ctx, e, v), true, nil
}

// classify runs the rules in order of decreasing certainty; the first match
// wins so each worktree is reported once, under its strongest reason.
func (s *scan) classify(ctx context.Context, e *entry) (verdict, bool, error) {
	if e.missing {
		v, err := s.missingVerdict(ctx, e)
		return v, err == nil, err
	}
	for _, r := range []rule{s.mergedRule, s.detachedRule, s.upstreamGoneRule, s.staleRule} {
		v, ok, err := r(ctx, e)
		if err != nil || ok {
			return v, ok, err
		}
	}
	return verdict{}, false, nil
}

// missingVerdict covers worktrees whose directory is gone: only git metadata
// remains, which the prune action removes without touching any file. That
// metadata is not harmless for a detached HEAD, though: HEAD and its reflog
// live in the admin dir, so a commit held by no ref would become unreachable.
// Such a worktree (and one whose containment is unknown) gets no action; the
// directory may only be unmounted or moved, which `git worktree repair` fixes.
func (s *scan) missingVerdict(ctx context.Context, e *entry) (verdict, error) {
	msg := "worktree directory is missing: " + e.wt.Path
	if e.wt.PruneReason != "" {
		msg += " (" + e.wt.PruneReason + ")"
	}
	v := verdict{
		conf:     findings.ConfidenceHigh,
		action:   findings.ActionPruneWorktrees,
		reason:   "the directory is gone; pruning only removes git metadata",
		evidence: []findings.Evidence{{Code: evMissing, Message: msg, Value: e.wt.Path}},
	}
	if !e.wt.Detached {
		return v, nil
	}
	where := ""
	if e.wt.Head != "" {
		var err error
		if where, err = s.containedIn(ctx, e.wt.Head); err != nil {
			return verdict{}, err
		}
	}
	if where != "" {
		return v, nil
	}
	v.conf, v.action = findings.ConfidenceLow, findings.ActionNone
	v.reason = "detached HEAD commits are not known to exist anywhere else and pruning would make them unreachable; " +
		"if the directory was moved or unmounted, run git worktree repair <new path>"
	v.evidence = append(v.evidence, findings.Evidence{
		Code: evHeadUnpushed, Message: "detached HEAD commits are not contained in the base or any remote", Value: e.wt.Head,
	})
	v.risks = []findings.RiskFlag{findings.RiskUnpushedCommits}
	return v, nil
}

func (s *scan) removeVerdict(conf findings.Confidence, reason string, ev findings.Evidence) verdict {
	return verdict{conf: conf, action: findings.ActionRemoveWorktree, reason: reason, evidence: []findings.Evidence{ev}}
}

// ctxErr returns the context error when the context ended: git failures then
// are cancellations, not facts about the worktree.
func ctxErr(ctx context.Context) error { return ctx.Err() }

// mergedRule reports a branch worktree whose branch is merged into the base.
// Base branches themselves are skipped: a worktree on `main` is not "merged".
// Unknown (a git error) is never treated as merged.
func (s *scan) mergedRule(ctx context.Context, e *entry) (verdict, bool, error) {
	b := e.wt.Branch
	if e.wt.Detached || b == "" || !s.hasBase || gitx.IsBaseBranch(s.base, s.cfg.Git.BaseBranches, b) {
		return verdict{}, false, nil
	}
	res, err := s.repo.MergedInto(ctx, s.base.FullRef, e.wt.BranchRef, s.squash)
	if err != nil || !res.Merged {
		return verdict{}, false, ctxErr(ctx)
	}
	ev := findings.Evidence{Code: evMerged, Message: fmt.Sprintf("branch %s is merged into %s", b, s.base.Ref), Value: s.base.Ref}
	if res.Method != gitx.MethodAncestor {
		ev = findings.Evidence{Code: evSquashMerged, Message: fmt.Sprintf("branch %s was squash- or rebase-merged into %s", b, s.base.Ref), Value: s.base.Ref}
	}
	return s.removeVerdict(findings.ConfidenceHigh, "branch is merged; removing the worktree keeps the branch and its commits", ev), true, nil
}

// detachedRule handles a detached HEAD: contained in the base or a remote
// makes it removable at medium confidence; contained nowhere marks the entry
// so only the stale rule may report it, and never with an action, because
// removing the worktree would leave those commits unreachable.
func (s *scan) detachedRule(ctx context.Context, e *entry) (verdict, bool, error) {
	if !e.wt.Detached || e.wt.Head == "" {
		return verdict{}, false, nil
	}
	where, err := s.containedIn(ctx, e.wt.Head)
	if err != nil {
		return verdict{}, false, err
	}
	if where == "" {
		e.unpushedDetached = true
		return verdict{}, false, nil
	}
	ev := findings.Evidence{Code: evHeadIn, Message: "detached HEAD is contained in " + where, Value: where}
	return s.removeVerdict(findings.ConfidenceMedium, "detached HEAD commits are contained elsewhere", ev), true, nil
}

// containedIn returns the base ref or remote branch that contains sha, or ""
// when none does (or when that is unknown).
func (s *scan) containedIn(ctx context.Context, sha string) (string, error) {
	if s.hasBase {
		ok, err := s.repo.IsAncestor(ctx, sha, s.base.FullRef)
		if err != nil {
			if cerr := ctxErr(ctx); cerr != nil {
				return "", cerr
			}
		} else if ok {
			return s.base.Ref, nil
		}
	}
	all, err := s.repo.ContainedInRemotes(ctx, sha)
	if err != nil || !all {
		return "", ctxErr(ctx)
	}
	remote, err := s.repo.RemoteContaining(ctx, sha)
	if err != nil || remote == "" {
		return "", ctxErr(ctx)
	}
	return remote, nil
}

// upstreamGoneRule reports a branch whose remote branch was deleted, provided
// every commit exists on a remote. A gone upstream alone does not prove the
// work was merged, so a branch with local-only commits is not a candidate.
func (s *scan) upstreamGoneRule(ctx context.Context, e *entry) (verdict, bool, error) {
	b, ok := s.branches[e.wt.Branch]
	if e.wt.Detached || !ok || !b.UpstreamGone {
		return verdict{}, false, nil
	}
	all, err := s.repo.ContainedInRemotes(ctx, e.wt.Head)
	if err != nil || !all {
		return verdict{}, false, ctxErr(ctx)
	}
	ev := findings.Evidence{Code: evUpstreamGone, Message: fmt.Sprintf("upstream %s was deleted and all commits exist on a remote", b.Upstream), Value: b.Upstream}
	return s.removeVerdict(findings.ConfidenceMedium, "upstream branch is gone and no commit is lost", ev), true, nil
}

// staleRule reports abandoned checkouts: both the HEAD commit and the newest
// file are older than the threshold. An unknown age is never stale. Unpushed
// branch commits do not matter (removal never deletes the branch), but a
// detached HEAD with unique commits is reported without an action.
func (s *scan) staleRule(ctx context.Context, e *entry) (verdict, bool, error) {
	wcfg := s.cfg.Detectors.Worktrees
	if !wcfg.IncludeStale {
		return verdict{}, false, nil
	}
	if err := s.summarize(ctx, e); err != nil {
		return verdict{}, false, err
	}
	head, err := s.headCommitTime(ctx, e)
	if err != nil || !e.sized || head.IsZero() || e.sum.NewestModTime.IsZero() {
		return verdict{}, false, err
	}
	commitAge, fileAge := s.env.AgeDays(head), s.env.AgeDays(e.sum.NewestModTime)
	minAge := max(wcfg.MinAgeDays, s.cfg.Thresholds.AgeFloor())
	if commitAge < minAge || fileAge < minAge {
		return verdict{}, false, nil
	}
	ev := findings.Evidence{
		Code:    evStale,
		Message: fmt.Sprintf("last commit %d days ago, newest file %d days ago", commitAge, fileAge),
		Value:   map[string]int{"commit_age_days": commitAge, "file_age_days": fileAge},
	}
	return s.staleVerdict(e, ev), true, nil
}

func (s *scan) staleVerdict(e *entry, ev findings.Evidence) verdict {
	if !e.unpushedDetached {
		return s.removeVerdict(findings.ConfidenceMedium, "worktree has not been touched for a long time", ev)
	}
	return verdict{
		conf:   findings.ConfidenceLow,
		action: findings.ActionNone,
		reason: "detached HEAD has commits that exist nowhere else; removing the worktree would make them unreachable",
		evidence: []findings.Evidence{ev, {
			Code: evHeadUnpushed, Message: "detached HEAD commits are not contained in the base or any remote", Value: e.wt.Head,
		}},
		risks: []findings.RiskFlag{findings.RiskUnpushedCommits},
	}
}

// summarize sizes the worktree once with a Fresh walk. The newest mtime feeds
// the abandoned-checkout rule, LastModified and recently_modified, and git
// status never lists ignored files (build output, env files, caches), so a
// cached mtime could hide an in-place edit of one of them. The walk contract
// (internal/walk, detect.Env.CacheDir) requires Fresh for exactly this. Only
// candidates are summarized, which bounds the cost. Sizing failures other than
// cancellation leave the summary unset: size 0 and never stale.
func (s *scan) summarize(ctx context.Context, e *entry) error {
	if e.sizeTried {
		return nil
	}
	e.sizeTried = true
	sum, err := walk.DirSize(ctx, e.path, walk.Options{CacheDir: s.env.CacheDir, Fresh: true})
	if err != nil {
		return ctxErr(ctx)
	}
	e.sum, e.sized = sum, true
	return nil
}

// headCommitTime returns the committer time of HEAD once, zero when unknown.
func (s *scan) headCommitTime(ctx context.Context, e *entry) (time.Time, error) {
	if e.headTried || e.wt.Head == "" {
		return e.headTime, nil
	}
	e.headTried = true
	t, err := s.repo.CommitTime(ctx, e.wt.Head)
	if err != nil {
		return time.Time{}, ctxErr(ctx)
	}
	e.headTime = t
	return t, nil
}

// flagBlocking adds the locked, operation, submodule and dirty classification
// to a candidate. Locked and an operation in progress always win and are never
// overridden. Dirty blocks unless --force is
// set and the candidate is otherwise removable, in which case the removal
// stays suggested with an explicit reason (the action trashes the directory).
func (s *scan) flagBlocking(ctx context.Context, e *entry, v *verdict) error {
	locked := e.wt.Locked
	if locked {
		v.risks = append(v.risks, findings.RiskWorktreeLocked)
		msg := "worktree is locked"
		if e.wt.LockReason != "" {
			msg += ": " + e.wt.LockReason
		}
		v.evidence = append(v.evidence, findings.Evidence{Code: evLocked, Message: msg, Value: e.wt.LockReason})
	}
	flagOperation(e, v)
	flagSubmodules(e, v)
	dirty := false
	if !e.missing {
		var err error
		if dirty, err = s.dirty(ctx, e); err != nil {
			return err
		}
	}
	if dirty {
		v.risks = append(v.risks, findings.RiskWorktreeDirty)
		v.evidence = append(v.evidence, findings.Evidence{Code: evDirty, Message: "worktree has uncommitted changes", Value: true})
	}
	s.decideAction(v, e, dirty)
	return nil
}

// flagOperation blocks a worktree paused in a rebase, merge, cherry-pick,
// revert or bisect: removing it would destroy the operation state (todo list,
// stop point), and its detached HEAD would otherwise look like a removable
// leftover.
func flagOperation(e *entry, v *verdict) {
	if e.missing || e.wt.Operation == "" {
		return
	}
	v.risks = append(v.risks, findings.RiskWorktreeOperation)
	v.evidence = append(v.evidence, findings.Evidence{
		Code: evOperation, Message: "a " + e.wt.Operation + " is in progress in the worktree", Value: e.wt.Operation,
	})
}

// flagSubmodules records initialized submodules. It is not a risk flag:
// nothing is at risk, the removal just cannot work (`git worktree remove`
// refuses such worktrees), so the finding stays visible without an action.
func flagSubmodules(e *entry, v *verdict) {
	if e.missing || !e.wt.HasSubmodules {
		return
	}
	v.evidence = append(v.evidence, findings.Evidence{
		Code: evSubmodules, Message: "the worktree has initialized submodules, which git worktree remove refuses", Value: true,
	})
}

// decideAction applies the blocking rules to the suggested action.
func (s *scan) decideAction(v *verdict, e *entry, dirty bool) {
	switch {
	case e.wt.Locked:
		v.action, v.reason = findings.ActionNone, "worktree is locked; unlock it with git worktree unlock first"
	case !e.missing && e.wt.Operation != "":
		v.action, v.reason = findings.ActionNone, "a "+e.wt.Operation+" is in progress in the worktree; finish or abort it first"
	case !e.missing && e.wt.HasSubmodules && v.action == findings.ActionRemoveWorktree:
		v.action, v.reason = findings.ActionNone, "the worktree has initialized submodules; deinitialize them (git submodule deinit --all) and remove the worktree manually"
	case dirty && v.action == findings.ActionRemoveWorktree && s.env.Force:
		v.reason = "forced: uncommitted changes will be moved to the trash, not deleted"
	case dirty && v.action == findings.ActionRemoveWorktree:
		v.action, v.reason = findings.ActionNone, "uncommitted changes; commit, stash or re-run with --force to trash the directory"
	}
}

// dirty reports uncommitted changes. When git cannot tell, the worktree is
// treated as dirty: unknown must never read as safe.
func (s *scan) dirty(ctx context.Context, e *entry) (bool, error) {
	d, err := s.repo.IsDirty(ctx, e.path)
	if err != nil {
		if cerr := ctxErr(ctx); cerr != nil {
			return false, cerr
		}
		return true, nil
	}
	return d, nil
}
