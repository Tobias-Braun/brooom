package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/buildinfo"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// cleanupSelection is what distinguishes the shortcut commands: the fixed
// detector selection of a command and the per-run switches that belong to it.
type cleanupSelection struct {
	// detectors are the detector names the command works on; the global
	// --detector flag can narrow but never widen them.
	detectors []string
	// userLocations scans the user-level tool locations (`brooom ai --user`).
	userLocations bool
	// label names the command in messages, e.g. "branches".
	label string
}

// machineFormats print parseable output only. They show findings in a dry
// run and cannot be combined with --apply, because the executor's plan and
// confirmation text would corrupt the stream.
var machineFormats = map[string]bool{"json": true, "ndjson": true, "plain": true}

// runCleanup is the shared body of the cleanup shortcut commands: a scan with
// a fixed detector selection followed by the executor. It only builds inputs
// and maps errors to exit codes; detection and cleanup live in detect and
// action. Nothing is modified unless af.apply is set.
func (a *app) runCleanup(cmd *cobra.Command, sel cleanupSelection, af applyFlags) error {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return err
	}
	format, err := resolveFormat(a.flags.format, cfg.Output.Format)
	if err != nil {
		return err
	}
	machine := machineFormats[format]
	if machine && af.apply {
		return usageError{fmt.Errorf("--format %s cannot be combined with --apply", format)}
	}
	strategy, err := parseTrashStrategy(af.trashStrategy)
	if err != nil {
		return err
	}
	names, err := a.resolveSelection(cfg, sel)
	if err != nil {
		return err
	}
	opts := scanOptions{detectors: names, userLocations: sel.userLocations, force: af.force}
	switch {
	case len(names) == 0:
		return a.nothingSelected(cfg, format)
	case machine:
		return a.runScan(cmd, opts)
	}
	return a.planAndRun(cmd, opts, af, strategy)
}

// nothingSelected handles a selection whose detectors are all disabled in the
// config. It must not start a scan: an empty detector list means "all
// detectors" to the pipeline. Machine formats still get a valid, empty
// report so scripts keep parsing.
func (a *app) nothingSelected(cfg *config.Config, format string) error {
	if !machineFormats[format] {
		fmt.Fprintln(a.io.Out, "nothing to clean")
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
// the names to scan. The flag is intersected with the selection (a shortcut
// cannot widen itself); an empty intersection is a usage error. Selected
// detectors that are not linked into this binary are an error, never a silent
// success, and detectors disabled in the config are dropped with a verbose
// note.
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
	if err := requireRegistered(names); err != nil {
		return nil, err
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

// requireRegistered fails for selected detectors that this build does not
// contain (their milestone has not landed).
func requireRegistered(names []string) error {
	for _, n := range names {
		if _, ok := detect.Get(n); !ok {
			return fmt.Errorf("detector %q is not available in this build", n)
		}
	}
	return nil
}

// planAndRun scans, then hands the findings to the executor: a dry run
// prints the plan, --apply confirms and executes.
func (a *app) planAndRun(cmd *cobra.Command, opts scanOptions, af applyFlags, strategy config.TrashStrategy) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := a.scan(ctx, opts, nil)
	if res == nil {
		return err
	}
	// The plan output has no room for scan problems, so they always go to stderr.
	a.logScanErrors(res.Report.Errors, true)
	if err != nil {
		return err
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	id := session.NewID(time.Now())
	resolver := newTrasherResolver(res.Config, strategy, dirs, id, a.io.Err)
	exec := action.NewExecutor(action.Options{
		Apply:     af.apply,
		Yes:       af.yes,
		Force:     af.force,
		IO:        action.IO{In: a.io.In, Out: a.io.Out, Err: a.io.Err},
		Store:     &session.Store{Dir: dirs.Sessions},
		Env:       buildActionEnv(res, af, resolver),
		Command:   a.commandLine(),
		SessionID: id,
		RerunHint: rerunHint(cmd),
	})
	result, err := exec.Run(ctx, res.Report.Findings)
	return mapExecutorError(result, err, af.apply)
}

// buildActionEnv assembles the action environment from the scan: the same
// guard and configuration the detectors saw, so actions cannot act outside the
// scanned scope.
func buildActionEnv(res *scanResult, af applyFlags, r *trasherResolver) *action.Env {
	return &action.Env{
		Config:     res.Config,
		Git:        res.Env.Git,
		Guard:      res.Guard,
		Trasher:    r.forDetector,
		TrasherFor: r.forStrategy,
		Force:      af.force,
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
		parts = append(parts, quoteArg(arg))
	}
	return strings.Join(parts, " ")
}

// quoteArg quotes an argument that contains whitespace, quotes or is empty.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\") {
		return s
	}
	return strconv.Quote(s)
}

// applyCommand is the command that executes what a dry run of cmd showed.
func applyCommand(cmd *cobra.Command) string {
	return cmd.CommandPath() + " --apply"
}

// rerunHint completes the executor's "dry run: nothing was changed; ... to
// execute" line.
func rerunHint(cmd *cobra.Command) string {
	return "re-run '" + applyCommand(cmd) + "'"
}

// applyHint is the single wording of "nothing was changed, here is how to act
// on it". Commands that cannot apply themselves (scan and the bare command)
// point at the sweep and the specific commands instead.
func applyHint(cmd *cobra.Command) string {
	if cmd.Flags().Lookup("apply") == nil {
		return "nothing was changed; run `brooom sweep --apply` or a specific command such as `brooom branches --apply`"
	}
	return "nothing was changed; run `" + applyCommand(cmd) + "` or `brooom sweep`"
}
