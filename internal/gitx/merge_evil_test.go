package gitx_test

import (
	"context"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestRebaseDetectionCoversMergeCommits reproduces the "evil merge": the
// branch holds a commit that was cherry-picked onto base plus a merge commit.
// The per-commit patch ids skip merges, so they all match base even when the
// merge commit carries content that base does not have; deleting the branch
// would then lose that content.
func TestRebaseDetectionCoversMergeCommits(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		// mergeExtra adds a file to the merge commit itself.
		mergeExtra bool
		wantMerged bool
	}{
		{name: "merge commit adds a file", mergeExtra: true},
		// The branch's net change equals one base commit, so the squash
		// path (which compares the whole mb..tip diff, merges included)
		// still and rightly reports it merged.
		{name: "merge commit without own changes", wantMerged: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			repo.Git("checkout", "-q", "-b", "evil")
			repo.Commit("a.txt", "a\n", "evil: a", at(1))
			repo.Checkout("main")
			repo.Commit("main.txt", "m\n", "main moves", at(2))
			repo.Checkout("evil")
			repo.GitAt(at(3), "merge", "--no-ff", "--no-commit", "refs/heads/main")
			if tc.mergeExtra {
				repo.WriteFile("extra.txt", "only in the merge\n")
				repo.Git("add", "extra.txt")
			}
			repo.GitAt(at(3), "commit", "-q", "-m", "merge main")
			repo.Checkout("main")
			repo.GitAt(at(4), "cherry-pick", repo.Git("rev-parse", "evil^1"))

			res, err := openRepo(t, execRunner(t), repo.Dir).MergedInto(ctx, "refs/heads/main", "refs/heads/evil", true)
			if err != nil {
				t.Fatal(err)
			}
			if res.Merged != tc.wantMerged {
				t.Errorf("MergedInto = %+v, want merged=%v", res, tc.wantMerged)
			}
		})
	}
}
