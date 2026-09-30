package cli

import "github.com/spf13/cobra"

func newGitCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Git history maintenance",
		Example: `  brooom git purge
  brooom git purge --gc --apply`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newGitPurgeCmd(a))
	return cmd
}

func newGitPurgeCmd(a *app) *cobra.Command {
	var af applyFlags
	var gc bool
	var reflogExpire, prune string
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Report git bloat and run gc, prune and reflog expiry (each opt-in)",
		Example: `  brooom git purge
  brooom git purge --gc --apply
  brooom git purge --reflog-expire 90.days.ago --prune 2.weeks.ago --apply`,
		Long: `Report loose objects, pack count, reflog size and large blobs, and run the
selected maintenance operations. Each operation is opt-in:

  --gc                    run 'git gc' (repacks objects; safe)
  --reflog-expire <date>  run 'git reflog expire --expire=<date> --all';
                          entries older than <date> can no longer be used to
                          recover deleted branches or reset commits
  --prune <date>          run 'git prune --expire=<date>'; unreachable
                          objects older than <date> are deleted for good

Dates use git's syntax, e.g. '90.days.ago' or '2026-01-01'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
	cmd.Flags().BoolVar(&gc, "gc", false, "run git gc")
	cmd.Flags().StringVar(&reflogExpire, "reflog-expire", "", "expire reflog entries older than this git date")
	cmd.Flags().StringVar(&prune, "prune", "", "prune unreachable objects older than this git date")
	addApplyFlags(cmd, &af)
	return cmd
}
