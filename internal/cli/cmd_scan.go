package cli

import "github.com/spf13/cobra"

// scanOptions narrows a scan to a subset of detectors or categories; the
// shortcut commands (branches, logs, artifacts, ai, ...) are scans with a
// preset selection.
type scanOptions struct {
	// detectors restricts the scan to these detector names (in addition to
	// the --detector flag).
	detectors []string //nolint:unused // read by runScan once the scan command is implemented
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

// runScan resolves scope and config, runs the selected detectors and prints
// the report in the requested format.
func (a *app) runScan(cmd *cobra.Command, opts scanOptions) error {
	return errNotImplemented
}
