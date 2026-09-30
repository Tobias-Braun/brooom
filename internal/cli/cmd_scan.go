package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
)

// scanOptions narrows a scan to a subset of detectors or categories; the
// shortcut commands (branches, logs, artifacts, ai, ...) are scans with a
// preset selection.
type scanOptions struct {
	// detectors restricts the scan to these detector names; it is combined
	// with the --detector flag by intersection.
	detectors []string
	// force is passed to the detectors as detect.Env.Force (--force of the
	// commands that can apply).
	force bool
	// userLocations turns on user_locations of the selected ai/logs detector for this
	// run only (`brooom ai --user`, `brooom logs --user`); the config file is never changed.
	userLocations bool
	// configOverlay adjusts the loaded configuration right after it was
	// read and before any per-root or per-repo layer (ForTarget), so a
	// .brooom.json still tightens on top of it. It receives a private copy.
	configOverlay func(*config.Config)
	// minConfidence drops findings below this confidence before they are
	// reported or planned; the zero value keeps everything.
	minConfidence findings.Confidence
}

// keep reports whether a finding passes the confidence floor.
func (o scanOptions) keep(f findings.Finding) bool {
	return o.minConfidence == "" || f.Confidence.Rank() >= o.minConfidence.Rank()
}

// filterStream wraps a streaming callback so it only sees findings that pass
// the confidence floor. A nil callback stays nil.
func (o scanOptions) filterStream(onFinding func(findings.Finding)) func(findings.Finding) {
	if onFinding == nil || o.minConfidence == "" {
		return onFinding
	}
	return func(f findings.Finding) {
		if o.keep(f) {
			onFinding(f)
		}
	}
}

// filter returns the findings that pass the confidence floor. Blocked
// findings are not treated specially: they stay when their confidence is high
// enough and the executor reports them as blocked.
func (o scanOptions) filter(in []findings.Finding) []findings.Finding {
	if o.minConfidence == "" {
		return in
	}
	out := make([]findings.Finding, 0, len(in))
	for _, f := range in {
		if o.keep(f) {
			out = append(out, f)
		}
	}
	return out
}

func newScanCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "scan",
		Short: "Scan for clutter and report findings (never modifies anything)",
		Long: `Scan the current repository (or, with --workspaces, every repository and
project below the configured roots) and report findings. Scanning never
modifies anything; use 'brooom sweep', a specific command with --apply, or
'brooom clean --from <file>' to act on findings.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runScan(cmd, scanOptions{})
		},
	}
}

// runScan validates the request (so a typo fails before a long scan), runs
// the scan and prints the report in the requested format. Formats that
// implement streamingFormatter print findings while the scan runs. On
// interruption the partial report is still rendered before the error is
// returned.
func (a *app) runScan(cmd *cobra.Command, opts scanOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := a.newScanRequest(opts)
	if err != nil {
		return err
	}
	formatter, err := output.Get(req.format)
	if err != nil {
		return usageError{err}
	}
	renderOpts := a.outputOptions(req.cfg)
	if sf, ok := formatter.(streamingFormatter); ok {
		return a.scanStreaming(ctx, req, sf, renderOpts)
	}
	res, err := a.execute(ctx, req, nil)
	if res == nil {
		return err
	}
	a.logScanErrors(res.Report.Errors, false)
	if werr := formatter.Write(a.io.Out, res.Report, renderOpts); werr != nil {
		return werr
	}
	if !machineFormats[req.format] && !a.flags.quiet && res.Report.Totals.Actionable > 0 {
		fmt.Fprintln(a.io.Out, applyHint(cmd))
	}
	return err
}

// scanStreaming runs the scan with the formatter's stream callbacks. The
// report is not rendered afterwards (the findings were already written), so
// scan errors go to stderr instead.
func (a *app) scanStreaming(ctx context.Context, req *scanRequest, sf streamingFormatter, opts output.Options) error {
	onFinding, finish := sf.NewStream(a.io.Out, opts)
	res, err := a.execute(ctx, req, onFinding)
	if ferr := finish(); ferr != nil && err == nil {
		err = ferr
	}
	if res != nil {
		a.logScanErrors(res.Report.Errors, true)
	}
	return err
}
