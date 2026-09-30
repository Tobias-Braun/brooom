package cli

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/buildinfo"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// cleanupSelection is what a sweep runs: the preset's detectors and the
// per-run switches that belong to it.
type cleanupSelection struct {
	// detectors are the detector names the command works on; the global
	// --detector flag can narrow but never widen them.
	detectors []string
	// label names the selection in messages, e.g. "sweep after-agents".
	label string
	// configOverlay is applied to the loaded configuration before the
	// per-root and per-repo layers (see scanOptions.configOverlay).
	configOverlay func(*config.Config)
	// keep drops findings it rejects before planning (a preset's confidence
	// floors); nil keeps everything.
	keep func(findings.Finding) bool
	// compact makes an applying run print one summary line ("2 worktrees
	// deleted, 5 merged branches removed. 4.2 GB reclaimed") instead of the
	// multi-line summary; --verbose brings it back.
	compact bool
}

// machineFormats print parseable output only. A run with one of them only
// reports, like --dry-run, and cannot be combined with --yes, because the
// executor's plan and summary text would corrupt the stream.
var machineFormats = map[string]bool{"json": true, "ndjson": true, "plain": true}

// runCleanup scans with a fixed detector selection and hands the findings to
// the executor, which shows the plan, asks and acts. It only builds inputs and
// maps errors to exit codes; detection and cleanup live in detect and action.
// Nothing is modified with --dry-run or a machine format.
func (a *app) runCleanup(cmd *cobra.Command, sel cleanupSelection, af applyFlags) error {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return err
	}
	resolve := resolveFormat
	if af.apply() {
		resolve = resolveActingFormat
	}
	format, err := resolve(a.flags.format, cfg.Output.Format)
	if err != nil {
		return err
	}
	// The acting format decides, not the scan's own resolution: an acting
	// run with a machine format in the config still prints human text.
	a.useProgress(format)
	machine := machineFormats[format]
	if machine && af.yes {
		return usageError{fmt.Errorf("--format %s only reports and cannot be combined with --yes", format)}
	}
	if machine {
		af.dryRun = true
	}
	strategy, err := parseTrashStrategy(af.trashStrategy)
	if err != nil {
		return err
	}
	names, err := a.resolveSelection(cfg, sel)
	if err != nil {
		return err
	}
	opts := scanOptions{
		detectors:     names,
		force:         af.force,
		configOverlay: sel.configOverlay,
		keep:          sel.keep,
	}
	switch {
	case len(names) == 0:
		return a.nothingSelected(cfg, format)
	case machine:
		return a.runScan(cmd, opts)
	}
	return a.planAndRun(cmd, opts, af, strategy, format, sel.compact)
}

// nothingSelected handles a selection whose detectors are all disabled in the
// config. It must not start a scan: an empty detector list means "all
// detectors" to the pipeline. Machine formats still get a valid, empty
// report so scripts keep parsing.
func (a *app) nothingSelected(cfg *config.Config, format string) error {
	if !machineFormats[format] {
		if !a.flags.quiet {
			fmt.Fprintln(a.io.Out, "nothing to clean")
		}
		return nil
	}
	formatter, err := output.Get(format)
	if err != nil {
		return usageError{err}
	}
	report := findings.NewReport(buildinfo.Get().Version, time.Now(), nil, nil, nil)
	return formatter.Write(a.io.Out, report, a.outputOptions(cfg))
}

// resolveSelection turns the command's detectors and the --detector flag into
// the names to scan. The flag is intersected with the selection (a preset
// cannot be widened); an empty intersection is a usage error. Detectors
// disabled in the config are dropped with a verbose note. Every detector is
// linked into the binary (internal/detectors/all), so a name that is not
// registered is a typo and a usage error.
func (a *app) resolveSelection(cfg *config.Config, sel cleanupSelection) ([]string, error) {
	names := slices.Sorted(slices.Values(sel.detectors))
	flagged, err := validateDetectorNames(a.flags.detectors)
	if err != nil {
		return nil, err
	}
	if len(flagged) > 0 {
		names = intersect(names, flagged)
		if len(names) == 0 {
			return nil, usageError{fmt.Errorf("--detector %s does not overlap with the detectors of `brooom %s` (%s)",
				strings.Join(flagged, ","), sel.label, strings.Join(sel.detectors, ","))}
		}
	}
	return a.enabledDetectors(cfg, names), nil
}

// enabledDetectors drops the detectors switched off in the global config and
// says so on stderr in verbose mode. Per-root switches are handled by the
// scan itself.
func (a *app) enabledDetectors(cfg *config.Config, names []string) []string {
	var out []string
	for _, n := range names {
		if !detectorEnabled(cfg, n) {
			a.progressf("skipping detector %s: disabled in the config", n)
			continue
		}
		out = append(out, n)
	}
	return out
}

// planAndRun scans, then hands the findings to the executor: a dry run
// renders the report in the requested format and appends the plan, otherwise
// the plan is confirmed and executed. The plan and prompts are fixed human
// text, but an explicit --format still selects how the report is shown before
// them: it was accepted and silently ignored. Without --format an acting run
// shows only the plan, not the report.
func (a *app) planAndRun(cmd *cobra.Command, opts scanOptions, af applyFlags, strategy config.TrashStrategy, format string, compact bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := a.scan(ctx, opts, nil)
	if res == nil {
		return err
	}
	if !af.apply() || a.flags.format != "" {
		if rerr := a.renderDryRunReport(res, format); rerr != nil {
			return rerr
		}
	} else {
		// The plan output has no room for scan problems, so they go to stderr.
		a.logScanErrors(res.Report.Errors, true)
	}
	if err != nil {
		return err
	}
	if ferr := scanFailure(res.Report); ferr != nil {
		return ferr
	}
	result, err := a.runExecutor(ctx, cmd, execInput{
		cfg: res.Config, git: res.Env.Git, guard: res.Guard, findings: res.Report.Findings,
		// --verbose keeps the per-item plan, so it is the opt-out of the brief summary.
		brief: compact && af.apply() && !a.flags.verbose,
	}, af, strategy)
	return mapExecutorError(result, err, af.apply())
}

// renderDryRunReport prints the scan report of a dry run with the formatter of
// the requested format, which also carries the scan errors in-band. The
// executor's plan follows it.
func (a *app) renderDryRunReport(res *scanResult, format string) error {
	formatter, err := output.Get(format)
	if err != nil {
		return usageError{err}
	}
	if !errorsInBand(format) {
		a.logScanErrors(res.Report.Errors, true)
	}
	return formatter.Write(a.io.Out, res.Report, a.outputOptions(res.Config))
}

// execInput is what the executor needs from whoever produced the findings: a
// scan (sweep) or a validated findings file (`clean --from`). The
// guard is always the one the findings were validated against, so actions
// cannot act outside that scope.
type execInput struct {
	cfg      *config.Config
	git      gitx.Runner
	guard    *scope.Guard
	findings []findings.Finding
	// brief selects the one-line summary instead of the per-item plan and
	// summary (see action.Options.Brief).
	brief bool
}

// runExecutor plans and, unless it is a dry run, confirms and executes the
// findings through the shared executor: confirmation, session manifest and summary are identical
// for every command.
func (a *app) runExecutor(ctx context.Context, cmd *cobra.Command, in execInput, af applyFlags, strategy config.TrashStrategy) (*action.Result, error) {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return nil, err
	}
	id := session.NewID(time.Now())
	resolver := newTrasherResolver(in.cfg, strategy, dirs, id, a.io.Err)
	exec := action.NewExecutor(action.Options{
		Apply:      af.apply(),
		Quiet:      a.flags.quiet,
		Brief:      in.brief,
		Yes:        af.yes,
		Force:      af.force,
		IO:         action.IO{In: a.io.In, Out: a.io.Out, Err: a.io.Err},
		StdinIsTTY: a.canPrompt, // as in undo, so a test can stand in for a terminal
		Store:      &session.Store{Dir: dirs.Sessions},
		Env:        buildActionEnv(in, af, resolver),
		Progress:   a.reporter(),
		Command:    a.commandLine(),
		Workspaces: a.flags.workspaces,
		UndoFlags:  a.scopeFlags(),
		SessionID:  id,
		RerunHint:  a.rerunHint(cmd),
	})
	return exec.Run(ctx, in.findings)
}

// buildActionEnv assembles the action environment: the same guard and
// configuration the findings were produced or validated with.
func buildActionEnv(in execInput, af applyFlags, r *trasherResolver) *action.Env {
	return newActionEnv(in.cfg, in.git, in.guard, af, r)
}

// newActionEnv is the single wiring of action.Env, shared by the cleanup
// commands (from a scan or a findings file) and undo (from a manifest and a
// resolved scope).
func newActionEnv(cfg *config.Config, git gitx.Runner, guard *scope.Guard, af applyFlags, r *trasherResolver) *action.Env {
	return &action.Env{
		Config:       cfg,
		Git:          git,
		Guard:        guard,
		Trasher:      r.forDetector,
		BeforeDelete: r.beforeDelete,
		TrasherFor:   r.forStrategy,
		Force:        af.force,
	}
}

// mapExecutorError maps the executor outcome to exit codes: a missing
// confirmation is a usage error (2); interruption and errors are 1, and so are
// failed steps, which are reported after the full summary was printed.
// Failures found while only planning a dry run are informational.
func mapExecutorError(res *action.Result, err error, applied bool) error {
	switch {
	case errors.Is(err, action.ErrConfirmationRequired):
		return usageError{err}
	case err != nil:
		return err
	case applied && res != nil && res.Failed > 0:
		return fmt.Errorf("%d step(s) failed; see the summary above", res.Failed)
	}
	return nil
}

// commandLine reconstructs the invocation for the session manifest, with
// arguments quoted the way a shell would need them.
func (a *app) commandLine() string {
	parts := []string{"brooom"}
	for _, arg := range a.args {
		parts = append(parts, a.quote(arg))
	}
	return strings.Join(parts, " ")
}

// quote quotes an argument for the shell of the host OS, so a suggested or
// recorded command can be pasted back (see findings.Quote). It is one helper
// for hints and manifests; goos only differs from the host in tests.
func (a *app) quote(s string) string {
	goos := a.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	return findings.QuoteFor(goos, s)
}
