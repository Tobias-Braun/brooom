package action

import (
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestDeleteBranchLocalUpstreamUsesDashD: a branch whose upstream is another
// local branch (remote ".") has no refs/remotes/ ref, so predicting git's
// `-d` check against "refs/remotes/main" always failed and the action fell
// back to -D. The prediction must use the real upstream ref.
func TestDeleteBranchLocalUpstreamUsesDashD(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/local-up")
	fx.repo.Git("merge", "-q", "--ff-only", "feat/local-up")
	fx.repo.Git("branch", "--set-upstream-to=main", "feat/local-up")

	step, _ := fx.mustApply(fx.finding("feat/local-up", "merged-branch", ""))
	if step.Command != "git branch -d feat/local-up" {
		t.Fatalf("command = %q, want -d through the local upstream", step.Command)
	}
	if fx.branchExists("feat/local-up") {
		t.Fatal("branch still exists")
	}
}

// TestDeleteBranchUnpushedCountsOnlyOwnCommits: the skip reason names the
// commits only this branch holds, not the history shared with main.
func TestDeleteBranchUnpushedCountsOnlyOwnCommits(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/two")
	fx.repo.Checkout("feat/two")
	fx.repo.Commit("two.txt", "2", "second", testutil.BaseTime.Add(2*time.Hour))
	fx.repo.Checkout("main")

	_, err := fx.plan(fx.finding("feat/two", "stale-branch", "in-remote"))
	wantBranchSkip(t, err, "2 commits exist only on this branch")
	if r := skipReason(err); strings.Contains(r, "on no remote") {
		t.Errorf("reason still uses the old wording: %q", r)
	}
}
