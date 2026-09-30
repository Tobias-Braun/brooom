package action

import (
	"context"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestDeleteBranchLocalBranchNamedLikeRemoteBase covers #88: a local branch
// literally named "origin/main" shadows the remote-tracking ref in short-name
// resolution. Its unmerged commit must not be "verified" against itself.
func TestDeleteBranchLocalBranchNamedLikeRemoteBase(t *testing.T) {
	fx := newBranchFixture(t)
	fx.repo.Git("checkout", "-q", "-b", "origin/main")
	fx.repo.Commit("local.txt", "x", "local only work", testutil.BaseTime.Add(time.Hour))
	fx.repo.Checkout("main")
	step, err := fx.plan(fx.finding("origin/main", "merged-branch", ""))
	if err == nil {
		_, err = fx.act.Apply(context.Background(), fx.env, step)
	}
	wantBranchSkip(t, err, "not fully merged")
	if !fx.branchExists("origin/main") {
		t.Fatal("branch with unmerged commits was deleted")
	}
}

// TestDeleteBranchTagWithBranchName covers #88: a tag named like the branch
// must not stand in for the branch tip during re-verification.
func TestDeleteBranchTagWithBranchName(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("rel")
	fx.repo.Git("tag", "rel", "main")
	step, err := fx.plan(fx.finding("rel", "merged-branch", "squash"))
	if err == nil {
		_, err = fx.act.Apply(context.Background(), fx.env, step)
	}
	wantBranchSkip(t, err, "not fully merged")
	if !fx.branchExists("rel") {
		t.Fatal("unmerged branch was deleted")
	}
}
