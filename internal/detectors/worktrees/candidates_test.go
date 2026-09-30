package worktrees_test

import (
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestWorktreeMergedIntoUnpushedLocalMain: origin/HEAD names origin/main, but
// the agent branch was merged into a local main that is ahead of it. The
// single primary base reported "Nothing to sweep" for such a worktree.
func TestWorktreeMergedIntoUnpushedLocalMain(t *testing.T) {
	repo := testutil.NewRepoWithRemote(t)
	repo.Git("checkout", "-q", "-b", "feat-agent", "main")
	repo.Commit("agent.txt", "agent", "agent work", testutil.BaseTime.Add(time.Hour))
	repo.Checkout("main")
	repo.GitAt(testutil.BaseTime.Add(2*time.Hour), "merge", "-q", "--no-ff", "-m", "merge agent", "feat-agent")
	wt := repo.AddWorktree("agent", "feat-agent")
	ageTree(t, wt, testutil.BaseTime)
	h := wtHarness(t, repo, wt)

	f := one(t, h.detect())
	if f.SuggestedAction.Type != findings.ActionRemoveWorktree || f.Ref != "feat-agent" {
		t.Fatalf("finding = %+v", f)
	}
	if ev := evidence(t, f, "merged_into"); ev.Value != "local main (not pushed)" {
		t.Errorf("merged_into value %v, want the matching local base", ev.Value)
	}
}
