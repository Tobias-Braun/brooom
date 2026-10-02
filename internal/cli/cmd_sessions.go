package cli

import "github.com/spf13/cobra"

func newSessionsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List the sessions that changed something",
		Example: `  brooom sessions
  brooom sessions --format json`,
		Long: `List the sessions recorded by the runs that changed something, newest
first: the id (for 'brooom undo <id>'), the repository or folder the run
worked on, the number of items it removed and the space it reclaimed.
Supports --format table (default), plain (session ids), json and ndjson (one
manifest per line). Read-only: nothing is modified.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSessions()
		},
	}
}
