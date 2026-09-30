package action

import (
	"context"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestRemoveWorktreeBareAnchor is the action half of issue #200: the finding
// of a bare plus linked worktrees layout names the bare directory as its
// repository, and removing the linked worktree must open and run git there
// instead of refusing it as "not a usable git repository".
func TestRemoveWorktreeBareAnchor(t *testing.T) {
	fx := newWTFixture(t)
	l := testutil.NewBareLayout(t)
	guard, err := scope.NewGuard(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	fx.env.Guard = guard

	repo, err := gitx.OpenAnchor(context.Background(), fx.git, l.Bare)
	if err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListWorktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wt, ok := findWorktree(list, l.Feat)
	if !ok {
		t.Fatalf("%s is not registered: %+v", l.Feat, list)
	}
	f := findings.Finding{
		ID: findings.NewID("worktrees", findings.KindWorktree, l.Feat, wt.Branch), Detector: "worktrees",
		Path: l.Feat, Ref: wt.Branch, Kind: findings.KindWorktree, SizeBytes: 100,
		SuggestedAction: findings.SuggestedAction{Type: findings.ActionRemoveWorktree},
		Meta:            map[string]string{"repo": l.Bare, "head": wt.Head, "branch": wt.Branch},
	}

	en, err := fx.apply(removeWorktree{}, f)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if en.Status != session.StatusApplied {
		t.Errorf("entry = %+v", en)
	}
	if exists(l.Feat) {
		t.Error("worktree directory still present")
	}
	if out := fx.gitOut(l.Bare, "worktree", "list", "--porcelain"); containsLine(out, "worktree "+l.Feat) {
		t.Errorf("worktree still registered:\n%s", out)
	}
}

// containsLine reports whether out has a line equal to want (git prints
// forward slashes on Windows, which the test layout paths already use there).
func containsLine(out, want string) bool {
	for _, l := range gitx.Lines(out) {
		if l == want {
			return true
		}
	}
	return false
}
