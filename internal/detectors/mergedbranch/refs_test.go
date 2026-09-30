package mergedbranch_test

import "testing"

// TestLocalBranchNamedLikeRemoteBaseIsNotMerged covers #88: a local branch
// named "origin/main" with unpushed commits must never be reported merged
// just because the short base ref "origin/main" resolves to it.
func TestLocalBranchNamedLikeRemoteBaseIsNotMerged(t *testing.T) {
	f := newFixture(t)
	f.publish()
	f.repo.Git("checkout", "-q", "-b", "origin/main")
	f.repo.Commit("local.txt", "x", "local only", at(50))
	f.repo.Checkout("main")
	if x := byRef(f.detect(), "origin/main"); x != nil {
		t.Fatalf("unmerged branch reported as merged: %+v", *x)
	}
}

// TestRenamedFreshBranchStaysHidden: renaming a freshly created branch adds a
// reflog entry, which must not make it look like used, merged work.
func TestRenamedFreshBranchStaysHidden(t *testing.T) {
	f := newFixture(t)
	f.publish()
	f.repo.Branch("scratch")
	f.repo.Git("branch", "-m", "scratch", "renamed")
	if x := byRef(f.detect(), "renamed"); x != nil {
		t.Fatalf("renamed fresh branch reported as merged: %+v", *x)
	}
}

// TestBranchWithTagOfSameNameIsNotMerged covers #88: git resolves tags before
// branches, so a tag on a merged commit must not make the branch look merged.
func TestBranchWithTagOfSameNameIsNotMerged(t *testing.T) {
	f := newFixture(t)
	f.feature("rel", "rel.txt")
	f.publish()
	f.repo.Git("tag", "rel", "main")
	if x := byRef(f.detect(), "rel"); x != nil {
		t.Fatalf("unmerged branch reported as merged: %+v", *x)
	}
}
