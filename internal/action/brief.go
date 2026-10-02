package action

import (
	"fmt"
	"io"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// briefKind names one kind of applied entry in the brief summary. Kinds are
// matched top to bottom, so the order here is also the order of the counts in
// the summary line. An empty detector matches every detector of the action.
type briefKind struct {
	action   findings.ActionType
	detector string
	one      string
	many     string
	verb     string
}

var briefKinds = []briefKind{
	{findings.ActionRemoveWorktree, "", "worktree", "worktrees", "deleted"},
	{findings.ActionPruneWorktrees, "", "missing worktree", "missing worktrees", "pruned"},
	{findings.ActionDeleteBranch, config.DetectorStaleBranch, "stale branch", "stale branches", "removed"},
	{findings.ActionDeleteBranch, config.DetectorMergedBranch, "merged branch", "merged branches", "removed"},
	{findings.ActionDeleteBranch, "", "branch", "branches", "removed"},
	{findings.ActionTrash, config.DetectorBuildArtifacts, "build artifact", "build artifacts", "removed"},
	{findings.ActionTrash, config.DetectorLogs, "log or runtime item", "log or runtime items", "removed"},
	{findings.ActionTrash, config.DetectorAIArtifacts, "AI artifact", "AI artifacts", "removed"},
	{findings.ActionGitGC, "", "git maintenance step", "git maintenance steps", "completed"},
	{findings.ActionGitPrune, "", "git maintenance step", "git maintenance steps", "completed"},
	{findings.ActionGitReflogExpire, "", "git maintenance step", "git maintenance steps", "completed"},
}

// briefFallback covers entries no kind above names (a new detector, a findings
// file from another tool), so they are still counted.
var briefFallback = briefKind{one: "item", many: "items", verb: "removed"}

func kindOf(e session.Entry) briefKind {
	for _, k := range briefKinds {
		if k.action == e.Action && (k.detector == "" || k.detector == e.Detector) {
			return k
		}
	}
	return briefFallback
}

// briefCounts renders "2 worktrees deleted, 5 stale branches removed" from the
// applied entries, or "" when nothing was applied. Kinds that share a noun and
// verb (the three git maintenance actions) are counted together.
func briefCounts(entries []session.Entry) string {
	type key struct{ one, verb string }
	counts := map[key]int{}
	for _, e := range entries {
		if e.Status == session.StatusApplied {
			k := kindOf(e)
			counts[key{k.one, k.verb}]++
		}
	}
	// Known kinds keep the table order, the fallback comes last. Emitting
	// clears the count, so kinds that share a noun and verb print once.
	var parts []string
	emit := func(k briefKind) {
		n := counts[key{k.one, k.verb}]
		if n == 0 {
			return
		}
		noun := k.many
		if n == 1 {
			noun = k.one
		}
		parts = append(parts, fmt.Sprintf("%d %s %s", n, noun, k.verb))
		counts[key{k.one, k.verb}] = 0
	}
	for _, k := range briefKinds {
		emit(k)
	}
	emit(briefFallback)
	return strings.Join(parts, ", ")
}

// renderBriefSummary prints the end-of-run summary of a brief run: what was
// removed, then how much disk that reclaimed, as the last line. Failures keep
// their full detail and skipped findings are counted, because both are what a
// user has to act on; per-item lines, git commands and recovery hints are left
// out (`brooom undo` restores, --dry-run lists the items). With quiet only the
// failures are printed.
func renderBriefSummary(w io.Writer, r *Result, quiet bool) {
	if len(r.Failures) > 0 {
		fmt.Fprintf(w, "failures (%d):\n", len(r.Failures))
		for _, f := range r.Failures {
			fmt.Fprintf(w, "  %s: %s\n", entryLabel(f.Path, f.Ref), output.Sanitize(f.Error))
		}
	}
	if quiet {
		return
	}
	kept := 0
	for _, s := range r.Skips {
		if s.Reason == reasonNotConfirmed {
			kept++
		}
	}
	if kept > 0 {
		fmt.Fprintf(w, "%s kept as you chose\n", plural(kept, "item"))
	}
	if skipped := r.Skipped - kept; skipped > 0 {
		fmt.Fprintf(w, "%s skipped (blocked or changed since the scan; run with --dry-run for details)\n", plural(skipped, "item"))
	}
	if r.Restorable() {
		fmt.Fprintf(w, "undo: brooom undo %s\n", undoCommandTail(r))
	}
	counts := briefCounts(r.Entries)
	if counts == "" {
		counts = "nothing cleaned"
	}
	fmt.Fprintf(w, "%s. %s reclaimed\n", counts, output.FormatSize(r.ReclaimedBytes))
}
