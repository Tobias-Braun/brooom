package cli

import "github.com/spf13/cobra"

func newCleanCmd(a *app) *cobra.Command {
	var af applyFlags
	var from string
	var ids []string
	cmd := &cobra.Command{
		Use:   "clean --from <findings.json>",
		Short: "Act on a reviewed findings file (from --format json)",
		Long: `Apply the suggested actions of a findings file produced with
'brooom scan --format json'. Edit or filter the file (or pass --id) to choose
what gets cleaned. Every finding is re-validated before anything is done.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "findings file ('-' for stdin)")
	cmd.Flags().StringSliceVar(&ids, "id", nil, "only act on these finding IDs (repeatable)")
	_ = cmd.MarkFlagRequired("from")
	addApplyFlags(cmd, &af)
	return cmd
}

func newUndoCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "undo [session-id]",
		Short: "Restore what a session removed (default: the latest session)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}

func newSessionsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "sessions [session-id]",
		Short: "List applied sessions, or show one session's manifest",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
}

func newPurgeCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Permanently delete quarantined sessions past their retention",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}
