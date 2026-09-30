package cli

import "github.com/spf13/cobra"

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
