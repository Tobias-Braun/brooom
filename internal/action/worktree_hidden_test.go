package action

import (
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestRemoveWorktreeHiddenEdits covers edits that `git status` cannot see
// (skip-worktree, assume-unchanged): they are uncommitted work like any other,
// so the delete strategy must refuse and the other strategies need --force.
func TestRemoveWorktreeHiddenEdits(t *testing.T) {
	flags := map[string]string{"skip-worktree": "--skip-worktree", "assume-unchanged": "--assume-unchanged"}
	for name, flag := range flags {
		setup := func(t *testing.T) (*wtFixture, string) {
			fx := newWTFixture(t)
			path := fx.add("feat", "feat")
			fx.gitOut(path, "update-index", flag, "README.md")
			testutil.WriteFile(t, path, "README.md", "# local override\n")
			return fx, path
		}

		t.Run(name+"/delete strategy refuses", func(t *testing.T) {
			fx, path := setup(t)
			fx.env.Force = true
			fx.strategy = config.StrategyDelete
			_, err := fx.plan(removeWorktree{}, fx.removeFinding(path))
			wantSkip(t, err, "refusing to permanently delete uncommitted work")
			if !exists(path) {
				t.Fatal("worktree was touched")
			}
		})

		t.Run(name+"/trash strategy needs force", func(t *testing.T) {
			fx, path := setup(t)
			_, err := fx.plan(removeWorktree{}, fx.removeFinding(path))
			wantSkip(t, err, "worktree has uncommitted changes; re-run with --force to trash it")
		})
	}
}
