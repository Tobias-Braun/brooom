package action

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// stackedBranches creates x1 with one commit and x2 on top of it, so x2 keeps
// x1's commits reachable for as long as it exists.
func (fx *branchFixture) stackedBranches() {
	fx.t.Helper()
	fx.featureBranch("x1")
	fx.repo.Git("checkout", "-q", "-b", "x2", "x1")
	fx.repo.Commit("x2.txt", "x2", "work on x2", testutil.BaseTime.Add(2*time.Hour))
	fx.repo.Checkout("main")
}

// TestRecoveryHintIgnoresBranchesDeletedInTheSameRun: x2 is built on x1 and
// both are deleted in one session; x1's hint used to say its commits are still
// reachable from x2, although x2 is gone afterwards.
func TestRecoveryHintIgnoresBranchesDeletedInTheSameRun(t *testing.T) {
	fx := newBranchFixture(t)
	fx.stackedBranches()
	fs := []findings.Finding{
		fx.finding("x1", "stale-branch", "forced"),
		fx.finding("x2", "stale-branch", "forced"),
	}
	res, err := NewExecutor(Options{
		Env: fx.env, Apply: true, Yes: true, Force: true,
		Store: session.NewStore(t.TempDir()), StdinIsTTY: func() bool { return false },
	}).Run(context.Background(), fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("entries = %+v, skips = %+v", res.Entries, res.Skips)
	}
	if got := fx.repo.Git("for-each-ref", "--format=%(refname)", "refs/heads"); got != "refs/heads/main" {
		t.Fatalf("branches left: %q", got)
	}
	for _, en := range res.Entries {
		if strings.Contains(en.RecoveryHint, "still reachable") {
			t.Errorf("hint of %s claims the commits stay reachable: %q", en.Ref, en.RecoveryHint)
		}
		checkRecoveryHint(t, en.RecoveryHint, "git branch "+en.Ref+" "+en.Undo["sha"])
	}
}

// TestRecoveryHintStillNamesSurvivingBranch is the other side: when x2 is not
// part of the run, it keeps x1's commits reachable and the hint says so.
func TestRecoveryHintStillNamesSurvivingBranch(t *testing.T) {
	fx := newBranchFixture(t)
	fx.stackedBranches()
	fx.env.Force = true
	f := fx.finding("x1", "stale-branch", "forced")
	_, en := fx.mustApply(f)
	checkReachableHint(t, en.RecoveryHint, "git branch x1 "+f.Meta["tip"], "x2")
}

func TestPlannedBranchDeletesOnlyCollectsBranchSteps(t *testing.T) {
	branch := findings.Finding{Path: "/r", Ref: "x1", SuggestedAction: findings.SuggestedAction{Type: findings.ActionDeleteBranch}}
	other := findings.Finding{Path: "/r", Ref: "x2", SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash}}
	got := plannedBranchDeletes([]Item{{Step: Step{Finding: branch}}, {Step: Step{Finding: other}}})
	if _, ok := got[plannedKey("/r", "refs/heads/x1")]; !ok || len(got) != 1 {
		t.Fatalf("planned = %v", got)
	}
}

// TestDeleteBranchPushedSquashMergedNeedsNoForce pins the other side of the
// heuristic gate: a squash-merged branch whose commits are all on a remote is
// deleted with -D without --force, and the reason names both facts. If the
// remote containment gate regressed to "heuristic alone is enough", the
// unpushed variant in deletebranch_policy_test.go would fail; if it regressed
// to "always needs --force", this test would.
func TestDeleteBranchPushedSquashMergedNeedsNoForce(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/sq")
	fx.repo.Git("push", "-q", "origin", "feat/sq")
	fx.repo.SquashMerge("feat/sq", "squash", testutil.BaseTime.Add(2*time.Hour))
	fx.repo.Push("main")
	f := fx.finding("feat/sq", "merged-branch", "squash")

	step, en := fx.mustApply(f)
	if step.Command != "git branch -D -- feat/sq" || en.Status != session.StatusApplied {
		t.Fatalf("step = %+v, entry = %+v", step, en)
	}
	for _, want := range []string{"squash-merged into origin/main", "all commits contained in remote-tracking branches"} {
		if !strings.Contains(step.Description, want) {
			t.Errorf("description %q lacks %q", step.Description, want)
		}
	}
	if fx.branchExists("feat/sq") {
		t.Fatal("branch still exists")
	}
}

// mergeIntoUnpushedMain merges name into main without pushing, so the local
// main is ahead of origin/main, and checks out another branch so that git's own
// -d check (against HEAD) cannot accept the deletion.
func (fx *branchFixture) mergeIntoUnpushedMain(name string) {
	fx.t.Helper()
	fx.repo.Git("branch", "work")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge "+name, name)
	fx.repo.Checkout("work")
}

// TestDeleteBranchMergedIntoUnpushedLocalBaseNeedsRemote: a merge into a local
// main that no remote has is a merge, but not remote-verified, so -D needs the
// commits on a remote (or --force) like a heuristic merge does.
func TestDeleteBranchMergedIntoUnpushedLocalBaseNeedsRemote(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.mergeIntoUnpushedMain("feat/x")
	f := fx.finding("feat/x", "merged-branch", "")

	_, err := fx.plan(f)
	wantBranchSkip(t, err, "local base that no remote has")
	if !fx.branchExists("feat/x") {
		t.Fatal("branch deleted although no remote has its commits")
	}

	fx.env.Force = true
	step, en := fx.mustApply(f)
	if step.Command != "git branch -D -- feat/x" || en.Status != session.StatusApplied {
		t.Fatalf("step = %+v, entry = %+v", step, en)
	}
}

// TestDeleteBranchMergedIntoUnpushedLocalBaseWhenPushed: with the branch on a
// remote the same merge justifies -D and the plan names the matching ref.
func TestDeleteBranchMergedIntoUnpushedLocalBaseWhenPushed(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.repo.Git("push", "-q", "origin", "feat/x")
	fx.mergeIntoUnpushedMain("feat/x")
	step, en := fx.mustApply(fx.finding("feat/x", "merged-branch", ""))
	if step.Command != "git branch -D -- feat/x" || en.Status != session.StatusApplied {
		t.Fatalf("step = %+v, entry = %+v", step, en)
	}
	if !strings.Contains(step.Description, "merged into local main (not pushed)") {
		t.Errorf("description = %q", step.Description)
	}
}

// deletedBranch applies a forced deletion of name and returns its entry.
func (fx *branchFixture) deletedBranch(name string) session.Entry {
	fx.t.Helper()
	fx.env.Force = true
	_, en := fx.mustApply(fx.finding(name, "stale-branch", "forced"))
	return en
}

// TestUndoReportsRefHierarchyCollisionsAsConflicts: git cannot hold feat and
// feat/child at once, in either direction; undo must not fail with git's
// "cannot lock ref" but report a conflict with an alternative name.
func TestUndoReportsRefHierarchyCollisionsAsConflicts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deleted string
		blocker string
		mention string
	}{
		{"directory in the way", "feat", "feat/child", "feat/child"},
		{"file in the way", "a/b", "a", "branch a exists"},
		{"file two levels up", "a/b/c", "a", "branch a exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newBranchFixture(t)
			fx.featureBranch(tc.deleted)
			en := fx.deletedBranch(tc.deleted)
			fx.repo.Git("branch", tc.blocker, "main")

			err := fx.act.Undo(context.Background(), fx.env, en)
			if !errors.Is(err, trash.ErrRestoreConflict) {
				t.Fatalf("Undo = %v, want a restore conflict", err)
			}
			for _, want := range []string{tc.mention, "git branch " + tc.deleted + "-restored " + en.Undo["sha"]} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if fx.branchExists(tc.deleted) {
				t.Error("undo created the branch despite the conflict")
			}
		})
	}
}

// TestUndoWithoutCollisionStillRestoresNestedNames guards against the
// collision check refusing harmless siblings such as feat/a and feat/b.
func TestUndoWithoutCollisionStillRestoresNestedNames(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/a")
	en := fx.deletedBranch("feat/a")
	fx.repo.Git("branch", "feat/b", "main")
	if err := fx.act.Undo(context.Background(), fx.env, en); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !fx.branchExists("feat/a") {
		t.Fatal("branch not restored")
	}
}
