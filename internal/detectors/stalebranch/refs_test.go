package stalebranch

import "testing"

// TestBranchWithTagOfSameNameIsStillReported covers #88: a tag named like the
// branch resolves first, so merged-branch's twin check must not treat the
// unmerged stale branch as merged and hide it from this detector.
func TestBranchWithTagOfSameNameIsStillReported(t *testing.T) {
	f := newFixture(t, true)
	f.branch("rel", f.daysAgo(100))
	f.repo.Git("tag", "rel", "main")
	got := f.mustDetect()
	if len(got) != 1 || got[0].Ref != "rel" {
		t.Fatalf("findings = %+v, want the stale branch rel", got)
	}
}
