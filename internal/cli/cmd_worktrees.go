package cli

import "github.com/spf13/cobra"

func newWorktreesCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "worktrees",
		Short: "Find (and remove) leftover git worktrees",
		Long: `Report worktrees whose branch is merged, stale or deleted, and worktree
metadata whose directory is gone. Dirty or locked worktrees are reported but
never suggested for removal. With --apply, worktrees are removed with
'git worktree remove' and metadata is pruned with 'git worktree prune'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}
