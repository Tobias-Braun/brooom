package worktrees

import (
	"context"
	"runtime"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// build turns a classified worktree into a finding. Meta always carries the
// main worktree path and the HEAD sha because the actions run git in the main
// worktree (a finding's Path is the linked worktree) and re-validate HEAD.
func (s *scan) build(ctx context.Context, e *entry, v verdict) findings.Finding {
	kind := findings.KindWorktree
	if e.missing {
		kind = findings.KindWorktreeMissing
	}
	last := s.lastModified(ctx, e)
	f := findings.Finding{
		ID:         findings.NewID(Name, kind, e.path, e.wt.Branch),
		Detector:   Name,
		Scope:      s.target.Scope,
		Path:       e.path,
		Kind:       kind,
		Ref:        e.wt.Branch,
		Confidence: v.conf,
		Evidence:   append(v.evidence, s.locationEvidence(e)...),
		RiskFlags:  v.risks,
		SuggestedAction: findings.SuggestedAction{
			Type:    v.action,
			Command: s.command(e, v.action),
			Reason:  v.reason,
		},
		Meta: s.meta(e),
	}
	if f.RiskFlags == nil {
		f.RiskFlags = []findings.RiskFlag{}
	}
	if e.sized {
		f.SizeBytes = e.sum.SizeBytes
	}
	if !last.IsZero() {
		f.LastModified = &last
		f.AgeDays = s.env.AgeDays(last)
	}
	return f
}

// lastModified is the newest file mtime of an existing worktree, falling back
// to the HEAD commit time (always for missing directories).
func (s *scan) lastModified(ctx context.Context, e *entry) time.Time {
	if e.sized && !e.sum.NewestModTime.IsZero() {
		return e.sum.NewestModTime
	}
	t, _ := s.headCommitTime(ctx, e)
	return t
}

func (s *scan) command(e *entry, a findings.ActionType) string {
	switch a {
	case findings.ActionPruneWorktrees:
		return "git worktree remove --force -- " + findings.ShellQuote(e.wt.Path)
	case findings.ActionRemoveWorktree:
		return "git worktree remove -- " + findings.ShellQuote(e.path)
	}
	return ""
}

func (s *scan) meta(e *entry) map[string]string {
	m := map[string]string{"repo": s.main, "head": e.wt.Head, "branch": e.wt.Branch}
	if e.wt.LockReason != "" {
		m["locked_reason"] = e.wt.LockReason
	}
	if e.wt.PruneReason != "" {
		m["prune_reason"] = e.wt.PruneReason
	}
	return m
}

// locationEvidence notes agent directory conventions. It is context only and
// never changes confidence or the suggested action.
func (s *scan) locationEvidence(e *entry) []findings.Evidence {
	loc := agentLocation(runtime.GOOS, s.main, e.path)
	if loc == "" {
		return nil
	}
	return []findings.Evidence{{
		Code:    evLocation,
		Message: "worktree lives in an agent worktree location (" + loc + ")",
		Value:   loc,
	}}
}
