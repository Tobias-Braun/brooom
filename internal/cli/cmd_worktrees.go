package cli

import (
	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
)

func newWorktreesCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "worktrees",
		Short: "Find (and remove) leftover git worktrees",
		Example: `  brooom worktrees
  brooom worktrees --apply
  brooom worktrees --workspaces --format tree`,
		Long: `Report worktrees whose branch is merged, worktrees whose branch ref is missing
(reported without an action) and worktree metadata whose directory is gone.
Worktrees are removable whatever their age, so the command can run right
after a large agent run: clean merged worktrees and detached worktrees whose
commits all landed on the base under other ids (rebased or squashed) are
removed, and worktrees left at a not yet started branch are kept. Dirty or
locked worktrees are reported but not suggested for removal (--force lifts
the dirty protection), and a worktree in use is never removed. Recent
activity is shown as an informational flag only. Abandoned worktrees are
reported only when detectors.worktrees.min_age_days is set above 0.
Linked worktrees outside the scanned scope (for example ../repo-wt) are
listed as informational findings with the hint to run 'brooom roots add
<parent>' or use --workspaces; they are never examined or removed. The plain
format is a bare path list for pipes and omits these informational findings;
use another format to see them. With --apply, a worktree directory is moved
to the trash (so ignored files such as .env stay recoverable) and then
deregistered from git; metadata of a missing directory is dropped without
touching other entries. Do not run 'git worktree remove' by hand instead: it
deletes ignored files permanently.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCleanup(cmd, cleanupSelection{detectors: []string{config.DetectorWorktrees}, label: "worktrees"}, af)
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}
