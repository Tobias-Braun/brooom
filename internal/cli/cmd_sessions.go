package cli

import "github.com/spf13/cobra"

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
		Long: `List the sessions recorded by --apply runs, or show one session in detail
(pass the full id or a unique prefix). Supports --format table (default) and
--format json. Read-only: nothing is modified.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSessions(args)
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
