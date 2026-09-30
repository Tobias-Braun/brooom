package worktrees_test

import (
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestBareAnchorLayout is the regression test for issue #200: with the bare
// repository plus linked worktrees layout the detector used to return
// nothing at all ("Nothing to sweep") because the first worktree is bare.
// The bare entry itself is never reported, the merged linked worktree is.
func TestBareAnchorLayout(t *testing.T) {
	l := testutil.NewBareLayout(t)
	// The branch must have been worked on and then merged: a branch still at
	// the base tip counts as unstarted and is never reported as merged.
	l.Repo.Git("-C", l.Feat, "commit", "-q", "--allow-empty", "-m", "work")
	l.Repo.Git("-C", l.Main, "merge", "-q", "--ff-only", "feat/x")
	ageTree(t, l.Feat, testutil.BaseTime)
	h := newHarness(t, l.Repo, l.Root)

	f := one(t, h.run(repoTarget(l.Main)))
	if f.Path != l.Feat || f.Ref != "feat/x" {
		t.Errorf("finding %+v, want the feat worktree", f)
	}
	if f.Meta["repo"] != l.Bare {
		t.Errorf("meta repo = %q, want the bare anchor %q", f.Meta["repo"], l.Bare)
	}
}
