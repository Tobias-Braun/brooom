package gitx_test

import (
	"context"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestUniqueCountIgnoresLocalBranches pins the difference between the safety
// gate (UnpushedCount, commits on no remote) and the display number
// (UniqueCount, commits only this branch holds) in a repository without any
// remote: history shared with main is not lost by deleting the branch.
func TestUniqueCountIgnoresLocalBranches(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	repo.Commit("a.txt", "a", "on main", at(1))
	repo.Git("checkout", "-q", "-b", "feat", "main")
	repo.Commit("f.txt", "f", "only on feat", at(2))
	repo.Git("branch", "sibling", "feat")
	repo.Git("checkout", "-q", "-b", "solo", "feat")
	repo.Commit("s1.txt", "s", "solo one", at(3))
	repo.Commit("s2.txt", "s", "solo two", at(4))
	repo.Checkout("main")
	handle := openRepo(t, execRunner(t), repo.Dir)

	tests := []struct {
		branch string
		unique int
	}{
		{"feat", 0}, // sibling holds the same tip
		{"sibling", 0},
		{"solo", 2}, // feat's commit is safe on feat and sibling
	}
	for _, tc := range tests {
		unpushed, err := handle.UnpushedCount(ctx, "refs/heads/"+tc.branch)
		if err != nil || unpushed <= tc.unique {
			t.Errorf("%s: UnpushedCount = %d, %v; must exceed the unique count %d", tc.branch, unpushed, err, tc.unique)
		}
		n, err := handle.UniqueCount(ctx, tc.branch)
		if err != nil || n != tc.unique {
			t.Errorf("%s: UniqueCount = %d, %v; want %d", tc.branch, n, err, tc.unique)
		}
	}
	if _, err := handle.UniqueCount(ctx, "no-such-branch"); err == nil {
		t.Error("unknown branch must be an error, not zero")
	}
}

func TestBranchCreatedOnly(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	repo.Commit("a.txt", "a", "base", at(1))
	repo.Branch("fresh")
	repo.Git("checkout", "-q", "-b", "used", "main")
	repo.Commit("u.txt", "u", "work", at(2))
	repo.Checkout("main")
	// Plumbing creates a branch without a reflog entry.
	repo.Git("update-ref", "refs/heads/plumbing", "main")
	handle := openRepo(t, execRunner(t), repo.Dir)

	tests := []struct {
		branch string
		want   bool
	}{
		{"fresh", true},
		{"used", false},
		{"plumbing", true},
	}
	for _, tc := range tests {
		got, err := handle.BranchCreatedOnly(ctx, tc.branch)
		if err != nil || got != tc.want {
			t.Errorf("%s: BranchCreatedOnly = %v, %v; want %v", tc.branch, got, err, tc.want)
		}
	}
	if _, err := handle.BranchCreatedOnly(ctx, "does-not-exist"); err == nil {
		t.Error("unknown branch must be an error")
	}
}

func TestLocalUpstream(t *testing.T) {
	ctx := context.Background()
	repo, _ := branchFixture(t)
	repo.Git("branch", "--set-upstream-to=main", "local-only")
	handle := openRepo(t, execRunner(t), repo.Dir)
	branches, err := handle.ListBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]gitx.Branch{}
	for _, b := range branches {
		byName[b.Name] = b
	}
	local, remote := byName["local-only"], byName["feat/x"]
	if !local.UpstreamIsLocal() || local.UpstreamRef != "refs/heads/main" || local.Upstream != "main" {
		t.Errorf("local-only upstream = %q / %q", local.Upstream, local.UpstreamRef)
	}
	if np, err := handle.NeverPushed(ctx, local); err != nil || !np {
		t.Errorf("a local upstream must not count as pushed: %v, %v", np, err)
	}
	if remote.UpstreamIsLocal() || remote.UpstreamRef != "refs/remotes/origin/feat/x" {
		t.Errorf("feat/x upstream ref = %q", remote.UpstreamRef)
	}
}
