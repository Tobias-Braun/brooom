package cli

import "github.com/spf13/cobra"

func newSweepCmd(a *app) *cobra.Command {
	var af applyFlags
	var preset string
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "The no-brainer: scan and clean with a safe preset",
		Long: `Scan with a preset and clean up what it finds.

Presets:
  safe        (default) only high-confidence findings: merged branches,
              prunable worktrees, OS junk, old logs, build artifacts of
              inactive projects
  standard    safe + stale branches, AI tool artifacts, caches
  aggressive  standard + lower age thresholds and git maintenance

Like every command, sweep is a dry run unless you pass --apply.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
	cmd.Flags().StringVarP(&preset, "preset", "p", "safe", "preset: safe, standard, aggressive")
	addApplyFlags(cmd, &af)
	return cmd
}
