package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/output"
)

func newReviewCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "review [path]",
		Short: "Decide one by one on dirty worktrees and unmerged branches",
		Example: `  brooom review
  brooom review ~/code
  brooom review --dry-run`,
		Long: `Walk through the work sweep leaves alone: worktrees with uncommitted changes
and local branches that are not merged, one at a time. For each it shows what
would be lost (changed and untracked files, commits that exist on no remote,
the last activity) and asks: d deletes it, k (or enter) keeps it, q stops and
deletes nothing at all.

Deleted worktrees go to the trash and are then deregistered; deleted branches
are removed with 'git branch -D' after their tip was recorded. Everything is
one session, so 'brooom undo' restores it. Items that can never be deleted
(the worktree or branch in use, a locked worktree, a protected branch) are
shown with the reason and skipped.

Without a terminal, or with --dry-run, review only lists the items.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a.setPath(args)
			return a.runReview(cmd, af)
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}

// reviewOverlay makes the branch and worktree detectors report every piece of
// unmerged or dirty work, whatever its age: review is where the user decides
// on it, so nothing may be withheld for being recent.
func reviewOverlay(c *config.Config) {
	c.Detectors.StaleBranch.Enabled = true
	c.Detectors.StaleBranch.MinAgeDays = 0
	c.Detectors.StaleBranch.IncludeUnpushed = true
	c.Detectors.Worktrees.IncludeStale = true
}

// reviewItem is one piece of work under review.
type reviewItem struct {
	f findings.Finding
	// deletable is false for findings no --force can act on.
	deletable bool
	summary   []string
}

// runReview scans with --force semantics (so overridable blocks come with an
// action), asks per item and hands the chosen findings to the shared
// executor, which re-validates each one before acting.
func (a *app) runReview(cmd *cobra.Command, af applyFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	a.useProgress(defaultFormat)
	strategy, err := parseTrashStrategy(af.trashStrategy)
	if err != nil {
		return err
	}
	res, err := a.scan(ctx, scanOptions{
		detectors:     []string{config.DetectorWorktrees, config.DetectorStaleBranch},
		force:         true,
		configOverlay: reviewOverlay,
	}, nil)
	if res == nil {
		return err
	}
	a.reporter().Pause()
	a.logScanErrors(res.Report.Errors, true)
	if err != nil {
		return err
	}
	items := reviewItems(ctx, res.Env.Git, res.Report.Findings)
	if len(items) == 0 {
		fmt.Fprintln(a.io.Out, "nothing to review: no dirty worktree and no unmerged branch")
		return nil
	}
	interactive := !af.dryRun && a.canPrompt()
	chosen, quit := a.askReview(items, interactive)
	switch {
	case af.dryRun:
		fmt.Fprintln(a.io.Out, "dry run: nothing was changed")
		return nil
	case !interactive:
		fmt.Fprintln(a.io.Out, "nothing was changed; run `brooom review` on a terminal to decide")
		return nil
	case quit:
		fmt.Fprintln(a.io.Out, "quit: nothing was changed")
		return nil
	case len(chosen) == 0:
		fmt.Fprintln(a.io.Out, "nothing to delete")
		return nil
	}
	// The decisions are made; the executor must not ask again, and force is
	// what makes the overridable blocks the user just saw actionable.
	af.yes, af.force = true, true
	result, err := a.runExecutor(ctx, cmd, execInput{
		cfg: res.Config, git: res.Env.Git, guard: res.Guard, findings: chosen,
	}, af, strategy)
	return mapExecutorError(result, err, true)
}

// reviewItems keeps the findings sweep refuses: worktrees with a blocking
// flag (dirty, unpushed, in use, locked) and every stale branch (not merged
// by definition), sorted by path and ref.
func reviewItems(ctx context.Context, git gitx.Runner, fs []findings.Finding) []reviewItem {
	var out []reviewItem
	for _, f := range fs {
		switch {
		case f.Detector == config.DetectorStaleBranch:
		case f.Detector == config.DetectorWorktrees && hasBlockingFlag(f):
		default:
			continue
		}
		it := reviewItem{f: f, deletable: f.SuggestedAction.Type != findings.ActionNone}
		it.summary = summarize(ctx, git, f)
		out = append(out, it)
	}
	slices.SortFunc(out, func(x, y reviewItem) int {
		return strings.Compare(x.f.Path+"\x00"+x.f.Ref, y.f.Path+"\x00"+y.f.Ref)
	})
	return out
}

func hasBlockingFlag(f findings.Finding) bool {
	return slices.ContainsFunc(f.RiskFlags, func(r findings.RiskFlag) bool { return r.Blocking() })
}

// maxListed is how many file names or commit subjects the summary shows.
const maxListed = 5

// summarize describes what deleting the item would lose. Git problems only
// shorten the summary; the decision stays with the user.
func summarize(ctx context.Context, git gitx.Runner, f findings.Finding) []string {
	var lines []string
	if f.Detector == config.DetectorWorktrees {
		lines = append(lines, worktreeChanges(ctx, git, f.Path)...)
		lines = append(lines, commitsOnlyHere(ctx, git, f.Path, "HEAD")...)
	} else {
		tip := f.Meta["tip"]
		if tip == "" {
			tip = "refs/heads/" + f.Ref
		}
		lines = append(lines, commitsOnlyHere(ctx, git, f.Path, tip)...)
	}
	if !f.LastModified.IsZero() {
		lines = append(lines, fmt.Sprintf("last activity %s (%s)", f.LastModified.Local().Format("2006-01-02"), ageText(f.AgeDays)))
	}
	if flags := blockingNames(f); flags != "" {
		lines = append(lines, "flags: "+flags)
	}
	return lines
}

// worktreeChanges counts changed and untracked files of a worktree and names
// the first few.
func worktreeChanges(ctx context.Context, git gitx.Runner, dir string) []string {
	if git == nil {
		return nil
	}
	out, err := git.Run(ctx, dir, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return nil
	}
	var changed, untracked int
	var names []string
	for _, l := range gitx.Lines(out) {
		if len(l) < 4 {
			continue
		}
		if strings.HasPrefix(l, "??") {
			untracked++
		} else {
			changed++
		}
		if len(names) < maxListed {
			names = append(names, output.Sanitize(l[3:]))
		}
	}
	if changed+untracked == 0 {
		return []string{"no uncommitted changes"}
	}
	line := fmt.Sprintf("%s changed, %s untracked", plural(changed, "file"), plural(untracked, "file"))
	if len(names) > 0 {
		line += ": " + strings.Join(names, ", ")
		if changed+untracked > len(names) {
			line += ", ..."
		}
	}
	return []string{line}
}

// commitsOnlyHere counts the commits of rev that no remote-tracking branch
// holds, the work that exists only in this repository, with the first
// subjects.
func commitsOnlyHere(ctx context.Context, git gitx.Runner, dir, rev string) []string {
	if git == nil {
		return nil
	}
	count, err := git.Run(ctx, dir, "rev-list", "--count", rev, "--not", "--remotes")
	if err != nil {
		return nil
	}
	n, _ := strconv.Atoi(strings.TrimSpace(count))
	if n == 0 {
		return []string{"no commits that exist only here"}
	}
	line := plural(n, "commit") + " on no remote"
	subjects, err := git.Run(ctx, dir, "log", "--format=%s", "-n", strconv.Itoa(3), rev, "--not", "--remotes")
	if err == nil {
		var s []string
		for _, l := range gitx.Lines(subjects) {
			s = append(s, strconv.Quote(output.Sanitize(l)))
		}
		if len(s) > 0 {
			line += ": " + strings.Join(s, ", ")
			if n > len(s) {
				line += ", ..."
			}
		}
	}
	return []string{line}
}

func blockingNames(f findings.Finding) string {
	var names []string
	for _, r := range f.RiskFlags {
		if r.Blocking() {
			names = append(names, string(r))
		}
	}
	return strings.Join(names, ", ")
}

func ageText(days int) string {
	if days == 0 {
		return "today"
	}
	return plural(days, "day") + " ago"
}

// askReview prints every item and, when interactive, asks per deletable item.
// It returns the chosen findings, and quit when the user stopped; a quit or
// the end of input discards every choice, so nothing is half applied.
func (a *app) askReview(items []reviewItem, interactive bool) ([]findings.Finding, bool) {
	r := bufio.NewReader(a.io.In)
	var chosen []findings.Finding
	for i, it := range items {
		a.printReviewItem(i+1, len(items), it)
		if !interactive {
			continue
		}
		if !it.deletable {
			fmt.Fprintf(a.io.Out, "  kept: %s\n\n", output.Sanitize(it.f.SuggestedAction.Reason))
			continue
		}
		switch reviewAnswer(r, a.io.Out) {
		case 'd':
			chosen = append(chosen, it.f)
		case 'q':
			return nil, true
		}
		fmt.Fprintln(a.io.Out)
	}
	return chosen, false
}

// reviewAnswer reads one decision: d deletes, k or an empty answer keeps, q or
// the end of input quits. Anything else asks again.
func reviewAnswer(r *bufio.Reader, out io.Writer) rune {
	for {
		fmt.Fprint(out, "  [d]elete / [k]eep / [q]uit? ")
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(out)
			return 'q'
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "d", "delete":
			return 'd'
		case "", "k", "keep":
			return 'k'
		case "q", "quit":
			return 'q'
		}
	}
}

func (a *app) printReviewItem(n, total int, it reviewItem) {
	f := it.f
	what := "worktree " + output.Sanitize(f.Path)
	if f.Detector == config.DetectorStaleBranch {
		what = "branch " + output.Sanitize(f.Ref)
	} else if b := f.Meta["branch"]; b != "" {
		what += " (" + output.Sanitize(strings.TrimPrefix(b, "refs/heads/")) + ")"
	}
	fmt.Fprintf(a.io.Out, "[%d/%d] %s\n", n, total, what)
	for _, l := range it.summary {
		fmt.Fprintf(a.io.Out, "  %s\n", l)
	}
}

// plural renders a count with its noun, "1 file" or "3 files".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
