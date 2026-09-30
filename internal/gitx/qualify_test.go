package gitx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestMergeQueriesRejectShortRefs covers #88: short names are ambiguous, so
// every merge query refuses them instead of guessing.
func TestMergeQueriesRejectShortRefs(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	featWithTwoCommits(repo)
	handle := openRepo(t, execRunner(t), repo.Dir)

	if _, err := handle.IsAncestor(ctx, "feat", "refs/heads/main"); !errors.Is(err, gitx.ErrUnqualifiedRef) {
		t.Errorf("IsAncestor short ancestor: %v", err)
	}
	if _, err := handle.MergedInto(ctx, "main", "refs/heads/feat", true); !errors.Is(err, gitx.ErrUnqualifiedRef) {
		t.Errorf("MergedInto short base: %v", err)
	}
	if _, err := handle.SquashMerged(ctx, "refs/heads/main", "feat"); !errors.Is(err, gitx.ErrUnqualifiedRef) {
		t.Errorf("SquashMerged short branch: %v", err)
	}
	// SHAs and HEAD stay accepted.
	if ok, err := handle.IsAncestor(ctx, repo.Git("rev-parse", "main"), "HEAD"); err != nil || !ok {
		t.Errorf("IsAncestor(sha, HEAD) = %v, %v", ok, err)
	}
}

// TestQualifiedRefsIgnoreShadowingTag covers #88: a tag named "feat" on main
// must not answer for refs/heads/feat.
func TestQualifiedRefsIgnoreShadowingTag(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	featWithTwoCommits(repo)
	repo.Git("tag", "feat", "main")
	handle := openRepo(t, execRunner(t), repo.Dir)

	res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/feat", true)
	if err != nil || res.Merged {
		t.Fatalf("MergedInto = %+v, %v; want not merged", res, err)
	}
}

// TestListBranchesUpstreamIsQualified covers #88: the upstream is parsed from
// %(upstream), so it is unambiguous even when a local branch is named
// "origin/main" (where %(upstream:short) prints "remotes/origin/main").
func TestListBranchesUpstreamIsQualified(t *testing.T) {
	repo := testutil.NewRepoWithRemote(t)
	repo.Git("branch", "origin/main", "main")
	handle := openRepo(t, execRunner(t), repo.Dir)

	branches, err := handle.ListBranches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range branches {
		if b.Name != "main" {
			continue
		}
		if b.Upstream != "origin/main" || b.UpstreamRef != "refs/remotes/origin/main" {
			t.Errorf("upstream = %q / %q", b.Upstream, b.UpstreamRef)
		}
		return
	}
	t.Fatal("main not listed")
}
