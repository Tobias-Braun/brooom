package action

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// actionPriority is the fixed execution order of action types that depend on
// each other. Git maintenance must run as reflog-expire, then prune, then gc:
// expiring reflog entries is what makes objects unreachable, prune removes
// them and gc repacks what is left. Running them alphabetically would gc
// before the objects are pruned and leave the repository bigger than needed.
//
// The list is the only place that encodes this order. Action types that are
// not listed rank after all listed ones and are ordered alphabetically among
// themselves (see compareKeys), so the order is total and unknown types never
// panic. If later actions need a fixed order, extend this list rather than
// adding per-action ordering hooks.
var actionPriority = []findings.ActionType{
	findings.ActionGitReflogExpire,
	findings.ActionGitPrune,
	findings.ActionGitGC,
}

// priorityRank returns the position of t in actionPriority, or the list
// length for unlisted types so they sort after every listed one.
func priorityRank(t findings.ActionType) int {
	if i := slices.Index(actionPriority, t); i >= 0 {
		return i
	}
	return len(actionPriority)
}

// orderKey is what plan ordering compares. Groups and steps are both sorted
// with compareKeys so their orders cannot diverge.
type orderKey struct {
	Detector string
	Action   findings.ActionType
	Path     string
	Ref      string
}

// compareKeys orders by detector name, action priority, action type name
// (the alphabetical fallback for unlisted types), path and ref.
func compareKeys(a, b orderKey) int {
	return cmp.Or(
		cmp.Compare(a.Detector, b.Detector),
		cmp.Compare(priorityRank(a.Action), priorityRank(b.Action)),
		cmp.Compare(a.Action, b.Action),
		cmp.Compare(a.Path, b.Path),
		cmp.Compare(a.Ref, b.Ref),
	)
}

// planned is a validated step together with its action type.
type planned struct {
	action findings.ActionType
	step   Step
}

// key uses the planned action type (not the step's finding) so sorting and
// grouping always agree on it.
func (p planned) key() orderKey {
	f := p.step.Finding
	return orderKey{f.Detector, p.action, f.Path, f.Ref}
}

// Plan validates findings and returns what would be done, without any side
// effect except reading. It never fails as a whole: findings that cannot be
// acted on end up in Plan.Skipped or Plan.Failed with a reason.
func (e *Executor) Plan(ctx context.Context, fs []findings.Finding) *Plan {
	p := &Plan{}
	cands := e.filterActionable(fs, p)
	cands = dedupe(cands)
	steps := e.planSteps(ctx, cands, p)
	steps = dropCovered(steps, p)
	slices.SortStableFunc(steps, func(a, b planned) int {
		return compareKeys(a.key(), b.key())
	})
	p.Groups = groupSteps(steps)
	return p
}

// filterActionable keeps findings that have an action and whose risk flags
// allow acting. Findings without an action are only counted; findings held
// back by a blocking flag are skipped with the flag named.
func (e *Executor) filterActionable(fs []findings.Finding, p *Plan) []findings.Finding {
	var out []findings.Finding
	for _, f := range fs {
		if !f.Actionable() {
			p.FlaggedOnly++
			continue
		}
		if !findings.Actionable(f.RiskFlags, e.env.Force) {
			p.Skipped = append(p.Skipped, Skip{f, blockedReason(f.RiskFlags, e.env.Force)})
			continue
		}
		out = append(out, f)
	}
	return out
}

// blockedReason names the blocking flags and says when --force cannot help.
func blockedReason(flags []findings.RiskFlag, force bool) string {
	var names []string
	overridable := true
	for _, r := range flags {
		if !r.Blocking() {
			continue
		}
		names = append(names, string(r))
		if !r.ForceOverridable() {
			overridable = false
		}
	}
	reason := "blocked by risk flag " + strings.Join(names, ", ")
	switch {
	case !overridable:
		reason += " (not overridable)"
	case !force:
		reason += " (use --force to override)"
	}
	return reason
}

// dedupe drops findings repeated by ID or by (action, path, ref); the first
// occurrence wins and duplicates are silently planned once.
func dedupe(fs []findings.Finding) []findings.Finding {
	seenID := map[string]bool{}
	seenKey := map[string]bool{}
	var out []findings.Finding
	for _, f := range fs {
		key := strings.Join([]string{string(f.SuggestedAction.Type), filepath.Clean(f.Path), f.Ref}, "\x00")
		if seenID[f.ID] || seenKey[key] {
			continue
		}
		seenID[f.ID], seenKey[key] = true, true
		out = append(out, f)
	}
	return out
}

// planSteps asks each finding's action to re-validate it.
func (e *Executor) planSteps(ctx context.Context, fs []findings.Finding, p *Plan) []planned {
	var out []planned
	// One open-file check for the whole plan instead of one per finding.
	ctx = e.batchOpenCheck(ctx, fs)
	for _, f := range fs {
		t := f.SuggestedAction.Type
		act, ok := e.opts.Lookup(t)
		if !ok {
			p.Skipped = append(p.Skipped, Skip{f, "action not available"})
			continue
		}
		step, err := act.Plan(ctx, e.env, f)
		switch {
		case err == nil:
			out = append(out, planned{t, step})
		case errors.Is(err, ErrSkipped):
			p.Skipped = append(p.Skipped, Skip{f, skipReason(err)})
		default:
			p.Failed = append(p.Failed, Skip{f, err.Error()})
		}
	}
	return out
}

// skipReason unwraps the reason from an error wrapping ErrSkipped, which
// actions build as fmt.Errorf("%w: reason", ErrSkipped).
func skipReason(err error) string {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, ErrSkipped.Error()+": "); ok {
		return rest
	}
	return msg
}

// dropCovered skips every step whose path lies strictly inside the path of
// a planned trash step: trashing the outer path already takes it along, and
// acting on both would fail the inner one. Which step is outer is decided by
// the paths alone, so the input order does not matter.
func dropCovered(steps []planned, p *Plan) []planned {
	var trashed []string
	for _, s := range steps {
		if s.action == findings.ActionTrash {
			trashed = append(trashed, filepath.Clean(s.step.Finding.Path))
		}
	}
	// Outermost first, so the reported cover is the top-level path.
	slices.SortFunc(trashed, func(a, b string) int { return cmp.Compare(len(a), len(b)) })
	var out []planned
	for _, s := range steps {
		path := filepath.Clean(s.step.Finding.Path)
		i := slices.IndexFunc(trashed, func(outer string) bool { return findings.IsWithin(outer, path) })
		if i < 0 {
			out = append(out, s)
			continue
		}
		p.Skipped = append(p.Skipped, Skip{s.step.Finding, fmt.Sprintf("covered by trashing %s", trashed[i])})
	}
	return out
}

// groupSteps folds the sorted steps into groups of one detector and action.
func groupSteps(steps []planned) []Group {
	var groups []Group
	for _, s := range steps {
		f := s.step.Finding
		n := len(groups)
		if n == 0 || groups[n-1].Detector != f.Detector || groups[n-1].Action != s.action {
			groups = append(groups, Group{Detector: f.Detector, Action: s.action})
			n++
		}
		groups[n-1].Items = append(groups[n-1].Items, Item{Step: s.step})
	}
	return groups
}
