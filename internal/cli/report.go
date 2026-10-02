package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
)

// scanOptions narrows a scan to a subset of detectors or categories; the
// sweep presets are scans with a preset selection.
type scanOptions struct {
	// detectors restricts the scan to these detector names; it is combined
	// with the --detector flag by intersection.
	detectors []string
	// force is passed to the detectors as detect.Env.Force (--force of the
	// commands that can apply).
	force bool
	// configOverlay adjusts the loaded configuration right after it was
	// read and before any per-root or per-repo layer (ForTarget), so a
	// .brooom.json still tightens on top of it. It receives a private copy.
	configOverlay func(*config.Config)
	// keep drops the findings it rejects (a preset's confidence floors)
	// before they are reported or planned; nil keeps everything.
	keep func(findings.Finding) bool
}

// passes reports whether a finding passes the keep filter.
func (o scanOptions) passes(f findings.Finding) bool {
	return o.keep == nil || o.keep(f)
}

// filterStream wraps a streaming callback so it only sees findings that pass
// the keep filter. A nil callback stays nil.
func (o scanOptions) filterStream(onFinding func(findings.Finding)) func(findings.Finding) {
	if onFinding == nil || o.keep == nil {
		return onFinding
	}
	return func(f findings.Finding) {
		if o.passes(f) {
			onFinding(f)
		}
	}
}

// filter returns the findings that pass the keep filter. Blocked findings are
// not treated specially: they stay when their confidence is high enough and
// the executor reports them as blocked.
func (o scanOptions) filter(in []findings.Finding) []findings.Finding {
	if o.keep == nil {
		return in
	}
	out := make([]findings.Finding, 0, len(in))
	for _, f := range in {
		if o.passes(f) {
			out = append(out, f)
		}
	}
	return out
}

// runScan is sweep with a machine format: it validates the request (so a
// typo fails before a long scan), runs the scan and prints the report in the
// requested format without acting. Formats that
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
	if !errorsInBand(req.format) {
		a.logScanErrors(res.Report.Errors)
	}
	if werr := formatter.Write(a.io.Out, res.Report, renderOpts); werr != nil {
		return werr
	}
	if err == nil {
		err = scanFailure(res.Report)
	}
	if err == nil {
		err = detectorFailure(res.Report)
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
		a.logScanErrors(res.Report.Errors)
		if err == nil {
			err = scanFailure(res.Report)
		}
		if err == nil {
			err = detectorFailure(res.Report)
		}
	}
	return err
}
