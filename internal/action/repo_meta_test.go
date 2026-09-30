package action

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// metaFixture is a wtFixture whose guard allows only one linked worktree and
// knows the main checkout solely as repository metadata, the shape of a run
// from inside a linked worktree.
func metaFixture(t *testing.T) (fx *wtFixture, own string) {
	t.Helper()
	fx = newWTFixture(t)
	own = fx.repo.AddWorktree("own", "own")
	g, err := scope.NewGuard(own)
	if err != nil {
		t.Fatal(err)
	}
	if fx.env.Guard, err = g.WithRepoMeta(fx.repo.Dir); err != nil {
		t.Fatal(err)
	}
	return fx, own
}

// TestRepoMetaCoversOnlyTheRepositoryLookup checks each consumer of the
// metadata location: worktree and branch actions may locate the repository in
// the main checkout, nothing else (files below it, sibling worktrees, git
// maintenance) resolves there.
func TestRepoMetaCoversOnlyTheRepositoryLookup(t *testing.T) {
	fx, own := metaFixture(t)
	sibling := filepath.Join(fx.repo.Dir, ".claude", "worktrees", "sibling")
	fx.repo.Git("worktree", "add", "-q", "-b", "sibling", sibling)
	ctx := context.Background()

	t.Run("remove-worktree of the own worktree plans", func(t *testing.T) {
		// Other skips (recent activity and so on) are fine here; only the
		// scope refusal would show the repository lookup failed.
		if _, err := fx.plan(removeWorktree{}, fx.removeFinding(own)); containsErr(err, errOutsideScope) {
			t.Fatalf("own worktree refused as out of scope: %v", err)
		}
	})
	t.Run("remove-worktree of a sibling below main is refused", func(t *testing.T) {
		_, err := fx.plan(removeWorktree{}, fx.removeFinding(sibling))
		if !errors.Is(err, ErrSkipped) || !containsErr(err, errOutsideScope) {
			t.Fatalf("Plan = %v, want a skip for %q", err, errOutsideScope)
		}
	})
	t.Run("delete-branch finds the repository in main only", func(t *testing.T) {
		if _, err := openRepo(ctx, fx.env, findings.Finding{Path: fx.repo.Dir}); err != nil {
			t.Errorf("main worktree refused: %v", err)
		}
		if _, err := openRepo(ctx, fx.env, findings.Finding{Path: filepath.Join(fx.repo.Dir, ".claude")}); err == nil {
			t.Error("a directory below main was accepted as repository")
		}
	})
	t.Run("git maintenance never accepts the main worktree", func(t *testing.T) {
		if _, err := openMaintRepo(ctx, fx.env, fx.repo.Dir); !errors.Is(err, ErrSkipped) {
			t.Errorf("openMaintRepo = %v, want a skip", err)
		}
	})
}

// containsErr reports whether the error text mentions want.
func containsErr(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}
