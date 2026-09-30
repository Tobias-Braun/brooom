package cli

import "github.com/spf13/cobra"

func newUndoCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "undo [session-id]",
		Short: "Restore what a session removed (default: the latest session)",
		Long: `Reverse an applied session using its manifest: trashed files are restored
from the trash or quarantine, deleted branches are recreated at their
recorded tip. Pass a full session id or a unique prefix; 'brooom sessions'
lists them. Without --apply this is a dry run.`,
		Example: `  brooom undo
  brooom undo 20260929-224501-3f9a
  brooom sessions`,
		Args: cobra.MaximumNArgs(1),
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
		Example: `  brooom sessions
  brooom sessions 20260929
  brooom sessions --format json`,
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
		Long: `Delete the quarantined files of sessions older than the configured
retention. This is the one command that removes data for good, so it is a
dry run unless you pass --apply and it asks before deleting.`,
		Example: `  brooom purge
  brooom purge --apply`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}
