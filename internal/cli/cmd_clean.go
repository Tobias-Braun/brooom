package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
)

// stdinSource is the value of --from that reads the report from stdin.
const stdinSource = "-"

// cleanOptions are the flags of `brooom clean` that are not apply flags.
type cleanOptions struct {
	from string
	ids  []string
	// user enables the user-level tool locations (`brooom ai --user`). It is
	// never inferred from the file: a findings file cannot widen the scope.
	user bool
	// detectors are the validated --detector names; empty selects all.
	detectors []string
}

func newCleanCmd(a *app) *cobra.Command {
	var af applyFlags
	var opts cleanOptions
	cmd := &cobra.Command{
		Use:   "clean --from <findings.json>",
		Short: "Act on a reviewed findings file (from --format json)",
		Example: `  brooom scan --format json > findings.json
  brooom clean --from findings.json
  brooom clean --from findings.json --id 8f2a41c7 --apply
  brooom scan --format json | brooom clean --from - --apply`,
		Long: `Apply the suggested actions of a findings file produced with
'brooom scan --format json'. Edit or filter the file (or pass --id) to choose
what gets cleaned. Every finding is re-validated before anything is done.

The file is untrusted input. The scope comes from this invocation (the current
repository, or the configured roots with --workspaces), never from the file:
findings outside it are refused and make the command exit with 1. User-level
locations are only accepted with --user. The action in the file only selects
which action to run; risk flags, sizes and ages in the file are never trusted,
and each finding is checked again against the live state before it is applied.

Your configuration applies as in a scan: -d/--detector selects which findings
are acted on (an unknown detector is a usage error), and findings of a detector
that is disabled, in the configuration or by a repository's .brooom.json, or
below an excluded directory are refused. Catalog-protected files such as .env
and .mcp.json are never removed. Git maintenance findings ignore any expiry
in the file and use the configured one.

A finding without a suggested action stays untouched, even with --force: scan
again with --force (export with 'brooom scan --force --format json') to get
an action for findings blocked by an overridable risk flag.

Use '--from -' to read the file from stdin. Without --apply this is a dry run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runClean(cmd, opts, af)
		},
	}
	cmd.Flags().StringVar(&opts.from, "from", "", "findings file ('-' for stdin)")
	cmd.Flags().StringSliceVar(&opts.ids, "id", nil, "only act on these finding IDs (repeatable, comma-separated)")
	cmd.Flags().BoolVar(&opts.user, "user", false, "also accept findings in user-level tool locations")
	addApplyFlags(cmd, &af)
	return cmd
}

// runClean is `brooom clean --from`: read the report, select by ID, rebuild
// the scope from this invocation, vet every finding against it and hand the
// accepted ones to the shared executor. It never trusts anything the file
// says about scope, risk or size.
func (a *app) runClean(cmd *cobra.Command, opts cleanOptions, af applyFlags) error {
	opts, strategy, err := a.checkCleanUsage(opts, af)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	sc, v, err := a.prepareClean(ctx, opts)
	if err != nil {
		return err
	}
	a.printVerdict(v)
	if err := sc.requireGit(v.accepted); err != nil {
		return err
	}
	if err := a.runVetted(ctx, cmd, sc, v, af, strategy); err != nil {
		return err
	}
	if len(v.refused) > 0 {
		return fmt.Errorf("%d finding(s) refused; see the list above (select findings with --id to exclude them)", len(v.refused))
	}
	return nil
}

// checkCleanUsage validates the flags before the file is read (usage errors,
// exit 2). --detector is checked against the registry exactly like scan does.
func (a *app) checkCleanUsage(opts cleanOptions, af applyFlags) (cleanOptions, config.TrashStrategy, error) {
	if len(a.flags.roots) > 0 && !a.flags.workspaces {
		return opts, "", usageError{fmt.Errorf("--root only narrows --workspaces; add --workspaces or drop --root")}
	}
	if opts.from == "" {
		return opts, "", usageError{fmt.Errorf("--from is required: give a findings file, or '-' for stdin")}
	}
	strategy, err := parseTrashStrategy(af.trashStrategy)
	if err != nil {
		return opts, "", err
	}
	opts.detectors, err = validateDetectorNames(a.flags.detectors)
	return opts, strategy, err
}

// prepareClean reads and selects the findings, rebuilds the scope of this
// invocation and vets every selected finding against it. Nothing is modified.
func (a *app) prepareClean(ctx context.Context, opts cleanOptions) (*cleanScope, verdict, error) {
	report, err := a.readFindingsSource(opts.from)
	if err != nil {
		return nil, verdict{}, err
	}
	selected, err := selectByID(report.Findings, splitIDs(opts.ids))
	if err != nil {
		return nil, verdict{}, err
	}
	selected = selectByDetector(selected, opts.detectors)
	cfg, _, err := a.loadConfig()
	if err != nil {
		return nil, verdict{}, err
	}
	sc, err := a.newCleanScope(ctx, cfg, opts.user)
	if err != nil {
		return nil, verdict{}, err
	}
	a.noteScopeDifference(report, sc)
	return sc, sc.vet(selected), nil
}

// runVetted executes the accepted findings, or reports that there is nothing
// to do without asking for a confirmation nobody needs.
func (a *app) runVetted(ctx context.Context, cmd *cobra.Command, sc *cleanScope, v verdict, af applyFlags, strategy config.TrashStrategy) error {
	if len(v.accepted) == 0 {
		fmt.Fprintln(a.io.Out, "nothing to clean")
		return nil
	}
	result, err := a.runExecutor(ctx, cmd, execInput{
		cfg: sc.cfg, git: sc.git, guard: sc.guard, findings: v.accepted,
	}, af, strategy)
	return mapExecutorError(result, err, af.apply)
}

// readFindingsSource loads the report from a file or, for "-", from stdin.
// Every error names the source.
func (a *app) readFindingsSource(from string) (*findings.Report, error) {
	name := from
	var r io.Reader
	if from == stdinSource {
		name, r = "stdin", a.io.In
		if r == nil {
			return nil, fmt.Errorf("read findings from %s: no input", name)
		}
	} else {
		f, err := os.Open(from)
		if err != nil {
			return nil, fmt.Errorf("read findings file: %w", err)
		}
		defer f.Close()
		r = f
	}
	report, err := findings.ReadReport(r)
	if err != nil {
		return nil, fmt.Errorf("findings from %s: %w", name, err)
	}
	return report, nil
}

// splitIDs flattens repeated and comma-separated --id values and drops empty
// entries and duplicates.
func splitIDs(values []string) []string {
	var out []string
	for _, v := range values {
		for _, id := range strings.Split(v, ",") {
			id = strings.TrimSpace(id)
			if id != "" && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// selectByID returns the findings to consider: all of them without --id,
// otherwise those with a wanted ID. IDs repeated in the file are kept once
// (the first wins), since a duplicate can only be a copy or a trick to
// inflate the plan. Any wanted ID that is not in the file is an error that
// lists all of them, and nothing is executed.
func selectByID(all []findings.Finding, wanted []string) ([]findings.Finding, error) {
	seen := map[string]bool{}
	var unique []findings.Finding
	for _, f := range all {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		unique = append(unique, f)
	}
	if len(wanted) == 0 {
		return unique, nil
	}
	var unknown []string
	for _, id := range wanted {
		if !seen[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown finding ID(s) not in the findings file: %s", strings.Join(unknown, ", "))
	}
	return slices.DeleteFunc(unique, func(f findings.Finding) bool { return !slices.Contains(wanted, f.ID) }), nil
}

// selectByDetector drops the findings of detectors that --detector did not
// select, like a scan that only ran those. They are skipped silently: not
// selecting them is the user's choice, not a refusal. The comparison uses the
// finding's own detector field, which only narrows the run.
func selectByDetector(fs []findings.Finding, detectors []string) []findings.Finding {
	if len(detectors) == 0 {
		return fs
	}
	return slices.DeleteFunc(fs, func(f findings.Finding) bool { return !slices.Contains(detectors, f.Detector) })
}

// noteScopeDifference tells, in verbose mode only, that the scopes recorded in
// the file differ from the scope of this run. The recorded scopes are
// informational; they never influence a decision.
func (a *app) noteScopeDifference(report *findings.Report, sc *cleanScope) {
	if !a.verboseOn() {
		return
	}
	for _, s := range report.Scopes {
		if !sc.knowsScope(s.Path) {
			a.progressf("note: the file was scanned in %s, which is not part of this run's scope; its findings there will be refused", s.Path)
		}
	}
}

// printVerdict lists what was refused and what was skipped before the plan,
// so the reasons are visible next to the summary.
func (a *app) printVerdict(v verdict) {
	out := a.io.Out
	if len(v.refused) > 0 {
		fmt.Fprintf(out, "refused findings (%d), not acted on because they do not fit this run's scope or are invalid:\n", len(v.refused))
		for _, r := range v.refused {
			fmt.Fprintf(out, "  %s  %s: %s\n", output.Sanitize(r.finding.ID), output.Sanitize(r.finding.Path), output.Sanitize(r.reason))
		}
	}
	if len(v.skipped) > 0 {
		fmt.Fprintf(out, "skipped findings (%d), nothing to act on:\n", len(v.skipped))
		for _, r := range v.skipped {
			fmt.Fprintf(out, "  %s  %s: %s\n", output.Sanitize(r.finding.ID), output.Sanitize(r.finding.Path), output.Sanitize(r.reason))
		}
	}
}
