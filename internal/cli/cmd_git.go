package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// The explanations are one text used by both the help and the dry run, so the
// warnings a user reads before opting in can never drift from the ones shown
// when the plan is printed.
const (
	explainGC = "git gc: repacks loose objects and packs (this can take a while and rewrites packs), deletes " +
		"unreachable objects older than the configured prune_expire, and also expires reflog entries per " +
		"gc.reflogExpire / gc.reflogExpireUnreachable (git defaults 90 / 30 days) and runs 'git worktree prune' " +
		"and 'git rerere gc'. Recovery points are lost too. Stash entries (refs/stash) are kept: Brooom " +
		"protects them even if gc.refs/stash.reflogExpire is set. Not restorable."
	explainReflog = "git reflog expire: removes reflog entries older than the date. Deleted branches and reset " +
		"commits older than that can no longer be recovered via the reflog (entries of unreachable commits " +
		"also follow gc.reflogExpireUnreachable). Stash entries (refs/stash) are uncommitted work and are " +
		"never expired; the plan counts the old ones that are kept. Not restorable."
	explainPrune = "git prune: deletes unreachable objects older than the date permanently; commits only " +
		"reachable through them cannot be recovered. Not restorable."
	dateSyntax = "Dates use git's syntax, e.g. '90.days.ago', '2.weeks.ago' or '2026-01-01'."
)

func newGitCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Git history maintenance",
		Example: `  brooom git purge
  brooom git purge --gc --apply`,
		Args: cobra.NoArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(newGitPurgeCmd(a))
	return cmd
}

// purgeFlags are the operations selected on the command line.
type purgeFlags struct {
	gc           bool
	reflogExpire string
	prune        string
	// setReflog and setPrune tell an explicit empty date (a mistake) from an
	// absent flag.
	setReflog, setPrune bool
}

func (p purgeFlags) any() bool { return p.gc || p.setReflog || p.setPrune }

func newGitPurgeCmd(a *app) *cobra.Command {
	var af applyFlags
	var pf purgeFlags
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Report git bloat and run gc, prune and reflog expiry (each opt-in)",
		Example: `  brooom git purge
  brooom git purge --gc --apply
  brooom git purge --reflog-expire 90.days.ago --prune 2.weeks.ago --apply`,
		Long: `Report loose objects, pack count, reflog size and large blobs, and run the
selected maintenance operations. Without a flag nothing but the report is
produced. Each operation is opt-in and independent, none can be undone, and
each is validated with git's own dry run before it is offered.

  --gc                    run 'git gc --prune=<prune_expire>' on repositories
                          with a loose-object or pack finding (healthy
                          repositories are never touched)
  --reflog-expire <date>  run 'git reflog expire --all' with <date> as expiry
                          (stash entries excepted) on every repository in
                          scope where it would remove entries
  --prune <date>          run 'git prune --expire=<date>' on every repository
                          in scope where it would delete objects

What they do:

  * ` + explainGC + `
  * ` + explainReflog + `
  * ` + explainPrune + `

` + dateSyntax + ` The order is fixed: reflog
expiry, then prune, then gc, so later steps see the expired reflog. A
repository with a rebase, merge, cherry-pick, revert or bisect in progress is
skipped. Large blobs need a history rewrite (git filter-repo), which Brooom
does not do. Use --workspaces for all repositories below the configured roots.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pf.setReflog = cmd.Flags().Changed("reflog-expire")
			pf.setPrune = cmd.Flags().Changed("prune")
			return a.runGitPurge(cmd, pf, af)
		},
	}
	cmd.Flags().BoolVar(&pf.gc, "gc", false, "run git gc on repositories with a loose-object or pack finding (repos without one are not touched)")
	cmd.Flags().StringVar(&pf.reflogExpire, "reflog-expire", "", "expire reflog entries older than this git date, e.g. 90.days.ago (removes recovery points)")
	cmd.Flags().StringVar(&pf.prune, "prune", "", "delete unreachable objects older than this git date, e.g. 2.weeks.ago (permanent)")
	addApplyFlags(cmd, &af)
	return cmd
}

// runGitPurge reports without flags and plans the selected operations with
// them. Dates are checked statically before anything runs and with git's dry
// run before the executor plans, both as usage errors.
func (a *app) runGitPurge(cmd *cobra.Command, pf purgeFlags, af applyFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if !pf.any() {
		return a.purgeReport(cmd, af)
	}
	strategy, err := a.purgeSetup(pf, af)
	if err != nil {
		return err
	}
	res, repos, err := a.purgeScan(ctx, pf, af)
	if err != nil {
		return err
	}
	fs := purgeFindings(res.Report.Findings, repos, pf)
	explainPurge(a.io.Out, pf)
	result, err := a.runExecutor(ctx, cmd, execInput{
		cfg: res.Config, git: res.Env.Git, guard: res.Guard, findings: fs,
	}, af, strategy)
	return mapExecutorError(result, err, af.apply)
}

// purgeScan resolves the scope and the repositories in it and validates the
// dates with git. The bloat detector only runs for --gc; the explicit dates
// need the repositories, not the findings.
func (a *app) purgeScan(ctx context.Context, pf purgeFlags, af applyFlags) (*scanResult, []purgeRepo, error) {
	opts := scanOptions{detectors: []string{config.DetectorGitBloat}, force: af.force, targetsOnly: !pf.gc}
	res, err := a.scan(ctx, opts, nil)
	if res == nil {
		return nil, nil, err
	}
	a.logScanErrors(res.Report.Errors, true)
	if err != nil {
		return nil, nil, err
	}
	repos, err := purgeRepos(ctx, res, a.io.Err)
	if err != nil {
		return nil, nil, err
	}
	return res, repos, validatePurgeDates(ctx, res, repos, pf)
}

// purgeSetup runs the checks that need no repository: dates, output format
// (the plan is text, so machine formats are refused) and trash strategy.
func (a *app) purgeSetup(pf purgeFlags, af applyFlags) (config.TrashStrategy, error) {
	if err := checkPurgeDates(pf); err != nil {
		return "", err
	}
	cfg, _, err := a.loadConfig()
	if err != nil {
		return "", err
	}
	format, err := resolveActingFormat(a.flags.format, cfg.Output.Format)
	if err != nil {
		return "", err
	}
	if machineFormats[format] {
		return "", usageError{fmt.Errorf("--format %s cannot be combined with --gc, --reflog-expire or --prune; drop the flags to list findings", format)}
	}
	return parseTrashStrategy(af.trashStrategy)
}

// purgeReport is the flag-less mode: the git-bloat findings in the chosen
// format, plus (for human formats) which flag performs what.
func (a *app) purgeReport(cmd *cobra.Command, af applyFlags) error {
	if af.apply {
		return usageError{errors.New("--apply needs an operation: --gc, --reflog-expire <date> or --prune <date>")}
	}
	if err := a.runScan(cmd, scanOptions{detectors: []string{config.DetectorGitBloat}}); err != nil {
		return err
	}
	cfg, _, err := a.loadConfig()
	if err != nil {
		return err
	}
	format, err := resolveFormat(a.flags.format, cfg.Output.Format)
	if err != nil {
		return err
	}
	if !machineFormats[format] && !a.flags.quiet {
		fmt.Fprintln(a.io.Out, "nothing was changed; choose what to run (each is opt-in, none can be undone):")
		fmt.Fprintln(a.io.Out, "  brooom git purge --gc                    gc repositories with loose-object or pack findings")
		fmt.Fprintln(a.io.Out, "  brooom git purge --reflog-expire <date>  remove reflog entries older than <date>")
		fmt.Fprintln(a.io.Out, "  brooom git purge --prune <date>          delete unreachable objects older than <date>")
		fmt.Fprintln(a.io.Out, dateSyntax+" Add --apply to execute.")
	}
	return nil
}

// checkPurgeDates rejects empty, option-like and otherwise malformed dates
// before any scan starts.
func checkPurgeDates(pf purgeFlags) error {
	for _, d := range []struct {
		set  bool
		flag string
		val  string
	}{{pf.setReflog, "--reflog-expire", pf.reflogExpire}, {pf.setPrune, "--prune", pf.prune}} {
		if !d.set {
			continue
		}
		if err := action.CheckDateSyntax(d.val); err != nil {
			return usageError{fmt.Errorf("%s: %w", d.flag, err)}
		}
	}
	return nil
}

// validatePurgeDates asks git (through a dry run in the first repository)
// whether it accepts the dates. Nothing has been planned or changed yet.
func validatePurgeDates(ctx context.Context, res *scanResult, repos []purgeRepo, pf purgeFlags) error {
	if len(repos) == 0 {
		return nil
	}
	for _, d := range []struct {
		set  bool
		flag string
		val  string
	}{{pf.setReflog, "--reflog-expire", pf.reflogExpire}, {pf.setPrune, "--prune", pf.prune}} {
		if !d.set {
			continue
		}
		if err := action.ValidateDate(ctx, res.Env.Git, repos[0].path, d.val); err != nil {
			if errors.Is(err, action.ErrInvalidDate) {
				return usageError{fmt.Errorf("%s: %w", d.flag, err)}
			}
			return err
		}
	}
	return nil
}

// purgeRepo is one repository in scope, identified by its main worktree.
type purgeRepo struct {
	path  string
	scope findings.Scope
}

// purgeRepos lists the repositories of the scan, one per common git
// directory: linked worktrees resolve to their main worktree and are folded
// into it, the same dedupe key as the git-bloat detector. Repositories whose
// main worktree lies outside the guard are left out.
func purgeRepos(ctx context.Context, res *scanResult, warn io.Writer) ([]purgeRepo, error) {
	if res.Env.Git == nil {
		return nil, gitx.ErrGitNotFound
	}
	var repos []purgeRepo
	seen := map[string]bool{}
	for _, t := range res.Targets {
		if t.Kind != scope.TargetRepo {
			continue
		}
		repo, err := gitx.Open(ctx, res.Env.Git, t.Path)
		if err != nil {
			if err := skipOpenError(warn, t.Path, err); err != nil {
				return nil, err
			}
			continue
		}
		if seen[repo.Common] {
			continue
		}
		seen[repo.Common] = true
		main, err := repo.MainWorktree(ctx)
		if err != nil {
			continue
		}
		path, ok := purgeRepoPath(res.Guard, main, t.Path)
		if !ok {
			continue
		}
		repos = append(repos, purgeRepo{path: path, scope: t.Scope})
	}
	return repos, nil
}

// purgeRepoPath picks the directory maintenance runs in. The main worktree is
// used when the guard allows it. When it is only known as repository metadata
// (a run from a linked worktree) the linked worktree itself stands in: it is in
// scope, and git maintenance there acts on the same shared repository, so
// purge keeps working without the main checkout becoming a general location.
func purgeRepoPath(g *scope.Guard, main, linked string) (string, bool) {
	if path, err := g.Resolve(main); err == nil {
		return path, true
	}
	if _, err := g.ResolveRepoMeta(main); err != nil {
		return "", false
	}
	path, err := g.Resolve(linked)
	return path, err == nil
}

// skipOpenError decides what a failed gitx.Open means for purge: not a
// repository and bare ones are skipped silently, dubious ownership is skipped
// with a visible line so it is never mistaken for a clean repository, and any
// other error is returned.
func skipOpenError(warn io.Writer, path string, err error) error {
	switch {
	case errors.Is(err, gitx.ErrUnsafeRepo):
		fmt.Fprintf(warn, "skipped: %s: %v\n", path, err)
		return nil
	case errors.Is(err, gitx.ErrNotRepo), errors.Is(err, gitx.ErrBareRepo):
		return nil
	}
	return err
}

// purgeFindings builds the finding set of the selected operations. --gc only
// takes the detector's gc suggestions (repositories without a loose-object
// or pack finding stay untouched). The explicit dates apply to every
// repository in scope: the user chose the date, and the plan's dry run counts
// decide per repository whether there is anything to do.
func purgeFindings(scanned []findings.Finding, repos []purgeRepo, pf purgeFlags) []findings.Finding {
	var out []findings.Finding
	if pf.gc {
		for _, f := range scanned {
			if f.Detector == config.DetectorGitBloat && f.SuggestedAction.Type == findings.ActionGitGC {
				out = append(out, f)
			}
		}
	}
	for _, r := range repos {
		if pf.setReflog {
			out = append(out, explicitFinding(r, findings.KindGitReflog, findings.ActionGitReflogExpire, pf.reflogExpire,
				gitx.ReflogExpireCommand(pf.reflogExpire), explainReflog))
		}
		if pf.setPrune {
			out = append(out, explicitFinding(r, findings.KindGitObjects, findings.ActionGitPrune, pf.prune,
				"git prune --expire="+pf.prune, explainPrune))
		}
	}
	return out
}

// explicitFinding synthesizes the finding for an operation the user asked
// for by flag. Its Ref keeps its ID apart from the detector's findings.
func explicitFinding(r purgeRepo, kind findings.Kind, act findings.ActionType, date, command, reason string) findings.Finding {
	ref := "explicit:" + string(act)
	return findings.Finding{
		ID:         findings.NewID(config.DetectorGitBloat, kind, r.path, ref),
		Detector:   config.DetectorGitBloat,
		Scope:      r.scope,
		Path:       r.path,
		Kind:       kind,
		Ref:        ref,
		Confidence: findings.ConfidenceHigh,
		Evidence:   []findings.Evidence{{Code: "explicit_request", Message: "requested on the command line with the date " + date, Value: date}},
		RiskFlags:  []findings.RiskFlag{},
		SuggestedAction: findings.SuggestedAction{
			Type: act, Args: map[string]string{"expire": date}, Command: command, Reason: reason,
		},
	}
}

// explainPurge prints what the selected operations do, before the plan.
func explainPurge(w io.Writer, pf purgeFlags) {
	// The notes follow the fixed execution order: reflog, prune, gc.
	var lines []string
	if pf.setReflog {
		lines = append(lines, explainReflog)
	}
	if pf.setPrune {
		lines = append(lines, explainPrune)
	}
	if pf.gc {
		lines = append(lines, explainGC)
	}
	fmt.Fprintln(w, "git maintenance permanently destroys data that is otherwise recoverable:")
	for _, l := range lines {
		fmt.Fprintln(w, "  - "+l)
	}
	fmt.Fprintln(w, strings.TrimSpace(dateSyntax))
}
