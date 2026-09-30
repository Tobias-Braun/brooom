package action

import (
	"strings"
	"testing"
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
	if step.Command != "git branch -d -- feat/local-up" {
		t.Fatalf("command = %q, want -d through the local upstream", step.Command)
	}
	if fx.branchExists("feat/local-up") {
		t.Fatal("branch still exists")
	}
	// The reason names the upstream that justified -d, not just "merged".
	if !strings.Contains(step.Description, "fully merged into upstream main") {
		t.Errorf("description = %q, want it to name the upstream", step.Description)
	}
}
