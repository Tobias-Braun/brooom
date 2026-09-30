package stalebranch

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// remoteState is what the repository says about where a branch's commits
// live.
type remoteState struct {
	// unpushed is the number of commits on no remote.
	unpushed int
	// contained is a remote-tracking ref containing the tip, "" if none.
	contained string
	// neverPushed: no upstream and no remote-tracking twin of the same name.
	neverPushed bool
	// upstreamHasTip: the upstream exists and the tip is an ancestor of it,
	// which is what `git branch -d` checks.
	upstreamHasTip bool
}

// assessRemote computes the remote signals of a branch. Any git failure is
// returned so the caller can skip the branch rather than guess: an unknown
// remote state must never look safe.
func (s *scan) assessRemote(ctx context.Context, b gitx.Branch) (remoteState, error) {
	var st remoteState
	var err error
	if st.unpushed, err = s.repo.UnpushedCount(ctx, b.Tip); err != nil {
		return st, err
	}
	if st.unpushed == 0 {
		if st.contained, err = s.repo.RemoteContaining(ctx, b.Tip); err != nil {
			return st, err
		}
	}
	if st.neverPushed, err = s.repo.NeverPushed(ctx, b); err != nil {
		return st, err
	}
	if b.Upstream != "" && !b.UpstreamGone {
		// A failed ancestor check only downgrades the command to -D, which
		// the action re-verifies anyway.
		ok, aerr := s.repo.IsAncestor(ctx, b.Tip, "refs/remotes/"+b.Upstream)
		st.upstreamHasTip = aerr == nil && ok
	}
	return st, nil
}

// assess turns a branch into a finding, or reports false when it is not
// reported (too young, base, protected, merged, omitted unpushed work or
// unassessable).
func (s *scan) assess(ctx context.Context, b gitx.Branch) (findings.Finding, bool) {
	if !s.isCandidate(b) || s.mergedSkip(ctx, b.Name) {
		return findings.Finding{}, false
	}
	st, err := s.assessRemote(ctx, b)
	if err != nil {
		return findings.Finding{}, false
	}
	if st.unpushed > 0 && !s.cfg.Detectors.StaleBranch.IncludeUnpushed {
		return findings.Finding{}, false
	}
	return s.build(b, st), true
}

// build assembles the finding for an assessed branch.
func (s *scan) build(b gitx.Branch, st remoteState) findings.Finding {
	blocking, info := s.riskFlags(b, st)
	flags := append(append([]findings.RiskFlag{}, blocking...), info...)
	date := b.Date
	age := s.env.AgeDays(date)
	return findings.Finding{
		ID:              findings.NewID(Name, findings.KindBranch, s.path, b.Name),
		Detector:        Name,
		Scope:           s.scope,
		Path:            s.path,
		Kind:            findings.KindBranch,
		Ref:             b.Name,
		LastModified:    &date,
		AgeDays:         age,
		Confidence:      confidence(b, st, blocking),
		Evidence:        evidence(b, st, age),
		SuggestedAction: s.action(b, st, blocking),
		RiskFlags:       flags,
		Meta:            s.meta(b),
	}
}

// riskFlags splits the flags into blocking ones (in a stable order) and
// informational ones.
func (s *scan) riskFlags(b gitx.Branch, st remoteState) (blocking, info []findings.RiskFlag) {
	if st.unpushed > 0 {
		blocking = append(blocking, findings.RiskUnpushedCommits)
	}
	if s.pr.HasOpenPR(b.Name) {
		blocking = append(blocking, findings.RiskHasOpenPR)
	}
	if b.WorktreePath != "" {
		blocking = append(blocking, findings.RiskCurrentBranch)
	}
	if b.UpstreamGone {
		info = append(info, findings.RiskUpstreamGone)
	}
	if st.neverPushed {
		info = append(info, findings.RiskNeverPushed)
	}
	return blocking, info
}

// confidence follows the table of the detector's specification: blocked
// findings are low because the risk is real, a fully contained branch whose
// upstream is gone is high, a never-pushed one is low and everything else
// medium.
func confidence(b gitx.Branch, st remoteState, blocking []findings.RiskFlag) findings.Confidence {
	switch {
	case len(blocking) > 0, st.neverPushed:
		return findings.ConfidenceLow
	case b.UpstreamGone:
		return findings.ConfidenceHigh
	default:
		return findings.ConfidenceMedium
	}
}

// evidence lists the stable-coded reasons behind the finding.
func evidence(b gitx.Branch, st remoteState, age int) []findings.Evidence {
	ev := []findings.Evidence{{
		Code:    "last_commit_age",
		Message: fmt.Sprintf("last commit %d days ago", age),
		Value:   age,
	}}
	if b.UpstreamGone {
		ev = append(ev, findings.Evidence{Code: "upstream_gone", Message: fmt.Sprintf("upstream %s no longer exists", b.Upstream), Value: b.Upstream})
	}
	if st.neverPushed {
		ev = append(ev, findings.Evidence{Code: "never_pushed", Message: "branch was never pushed to a remote"})
	}
	if st.contained != "" {
		ev = append(ev, findings.Evidence{Code: "contained_in_remote", Message: fmt.Sprintf("all commits are contained in %s", st.contained), Value: st.contained})
	}
	if st.unpushed > 0 {
		ev = append(ev, findings.Evidence{Code: "unpushed_commits", Message: fmt.Sprintf("%d commits exist on no remote", st.unpushed), Value: st.unpushed})
	}
	return ev
}

// meta carries the tip (required by the delete action), the upstream, the
// base when known and how the open pull request check went.
func (s *scan) meta(b gitx.Branch) map[string]string {
	m := map[string]string{"tip": b.Tip, "open_pr_check": s.prCheck}
	if b.Upstream != "" {
		m["upstream"] = b.Upstream
	}
	if s.hasBase {
		m["base"] = s.base.Ref
	}
	return m
}

// action chooses the suggested action. Unblocked findings suggest deleting
// with the in-remote verification. Blocking flags yield none, except that
// --force with only force-overridable flags yields a forced -D; a checked-out
// branch is never overridable.
func (s *scan) action(b gitx.Branch, st remoteState, blocking []findings.RiskFlag) findings.SuggestedAction {
	switch {
	case len(blocking) == 0:
		return safeAction(b, st)
	case s.env.Force && findings.Actionable(blocking, true):
		return findings.SuggestedAction{
			Type:    findings.ActionDeleteBranch,
			Args:    map[string]string{"verified": "forced"},
			Command: "git branch -D " + b.Name,
			Reason:  "forced: " + joinFlags(blocking),
		}
	default:
		return findings.SuggestedAction{Type: findings.ActionNone, Reason: blockedReason(b, st, blocking)}
	}
}

// safeAction suggests -d when git itself can verify the deletion through the
// upstream and -D otherwise, since every commit is on a remote anyway.
func safeAction(b gitx.Branch, st remoteState) findings.SuggestedAction {
	in := "remote-tracking branches"
	if st.contained != "" {
		in = st.contained
	}
	flag, why := "-D", "all commits are contained in "+in+"; git cannot verify this through an upstream, so -D is needed"
	if st.upstreamHasTip {
		flag, why = "-d", "the tip is contained in upstream "+b.Upstream
	} else if b.UpstreamGone {
		why = "upstream " + b.Upstream + " is gone but all commits are contained in " + in + "; -D is needed"
	}
	return findings.SuggestedAction{
		Type:    findings.ActionDeleteBranch,
		Args:    map[string]string{"verified": "in-remote"},
		Command: "git branch " + flag + " " + b.Name,
		Reason:  why,
	}
}

// blockedReason explains every blocking flag in one sentence-list.
func blockedReason(b gitx.Branch, st remoteState, blocking []findings.RiskFlag) string {
	var parts []string
	for _, f := range blocking {
		switch f {
		case findings.RiskUnpushedCommits:
			parts = append(parts, fmt.Sprintf("%d commits exist on no remote; deleting would lose them (re-run with --force to override)", st.unpushed))
		case findings.RiskHasOpenPR:
			parts = append(parts, "an open pull request uses this branch (re-run with --force to override)")
		case findings.RiskCurrentBranch:
			parts = append(parts, "the branch is checked out in "+b.WorktreePath+" and cannot be deleted")
		}
	}
	return strings.Join(parts, "; ")
}

// joinFlags renders flags as a comma separated list.
func joinFlags(flags []findings.RiskFlag) string {
	names := make([]string, len(flags))
	for i, f := range flags {
		names[i] = string(f)
	}
	return strings.Join(names, ", ")
}
