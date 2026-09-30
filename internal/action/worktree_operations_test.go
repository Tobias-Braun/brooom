package action

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// markInProgress simulates a paused operation in the linked worktree at path
// by creating the marker git would leave in its private git directory.
func (fx *wtFixture) markInProgress(path, marker string) {
	fx.t.Helper()
	admin := fx.gitOut(path, "rev-parse", "--absolute-git-dir")
	if err := os.MkdirAll(filepath.Join(admin, marker), 0o755); err != nil {
		fx.t.Fatal(err)
	}
}

// TestRemoveWorktreeRefusesOperationInProgress: a worktree paused in a
// rebase or bisect must never be removed, with or without --force, because
// removal destroys the todo list and the stop point.
func TestRemoveWorktreeRefusesOperationInProgress(t *testing.T) {
	for _, force := range []bool{false, true} {
		fx := newWTFixture(t)
		fx.env.Force = force
		p := fx.add("wt", "")
		f := fx.removeFinding(p)
		fx.markInProgress(p, "rebase-merge")

		_, err := fx.plan(removeWorktree{}, f)
		wantSkip(t, err, "rebase is in progress")
		en, err := removeWorktree{}.Apply(context.Background(), fx.env, Step{Finding: f})
		if err != nil || en.Status != session.StatusSkipped {
			t.Errorf("force=%v: Apply = %+v, %v; want a skipped entry", force, en, err)
		}
		if !fx.registered(p) || !exists(p) {
			t.Errorf("force=%v: worktree with a rebase in progress was removed", force)
		}
	}
}

// TestRemoveWorktreeRefusesSubmodules: git worktree remove always fails on a
// worktree with initialized submodules, so it is refused up front with a
// reason instead of failing on every sweep.
func TestRemoveWorktreeRefusesSubmodules(t *testing.T) {
	fx := newWTFixture(t)
	sub := testutil.NewRepo(t)
	fx.repo.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "sub")
	fx.repo.CommitAll("add submodule", testutil.BaseTime)
	p := fx.add("wt", "feat")
	fx.gitOut(p, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
	f := fx.removeFinding(p)

	_, err := fx.plan(removeWorktree{}, f)
	wantSkip(t, err, "submodules")
	en, err := removeWorktree{}.Apply(context.Background(), fx.env, Step{Finding: f})
	if err != nil || en.Status != session.StatusSkipped {
		t.Errorf("Apply = %+v, %v; want a skipped entry", en, err)
	}
	if !fx.registered(p) {
		t.Error("worktree with submodules must stay registered")
	}
}

// TestDeleteBranchRefusesBranchPausedInRebase: while a rebase runs HEAD is
// detached and %(worktreepath) is empty, yet git refuses to delete the
// branch ("used by worktree"). It must be skipped, not failed at apply.
func TestDeleteBranchRefusesBranchPausedInRebase(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/paused")
	wt := fx.repo.AddWorktree("paused", "")
	admin := fx.repo.Git("-C", wt, "rev-parse", "--absolute-git-dir")
	testutil.WriteFile(t, filepath.Join(admin, "rebase-merge"), "head-name", "refs/heads/feat/paused\n")

	_, err := fx.plan(fx.finding("feat/paused", "merged-branch", ""))
	wantBranchSkip(t, err, "checked out")
}
