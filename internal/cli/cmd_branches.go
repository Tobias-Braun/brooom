package cli

import "github.com/spf13/cobra"

func newBranchesCmd(a *app) *cobra.Command {
	var af applyFlags
	var stale, merged bool
	cmd := &cobra.Command{
		Use:   "branches",
		Short: "Find (and delete) stale and merged local branches",
		Long: `Report local branches that are merged into the base branch (including
squash merges) or stale (no commits for a long time, upstream gone or never
pushed). With --apply they are deleted with 'git branch -d' (-D with
--force). Every deleted branch's tip commit is recorded, so 'brooom undo'
can recreate it; the manual recovery command is printed as well.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
	cmd.Flags().BoolVar(&stale, "stale", false, "only stale branches")
	cmd.Flags().BoolVar(&merged, "merged", false, "only merged branches")
	addApplyFlags(cmd, &af)
	return cmd
}
