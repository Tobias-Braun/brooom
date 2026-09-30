package action

import (
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestDeleteBranchSquashOnlyUnpushedNeedsForce pins the safety policy: a
// branch that only the patch-id heuristic calls merged, and whose commits
// exist on no remote, is deleted with -D only under --force, whatever the
// finding claims. Ancestry and remote containment stay unforced.
func TestDeleteBranchSquashOnlyUnpushedNeedsForce(t *testing.T) {
	squash := func(fx *branchFixture) {
		fx.repo.SquashMerge("feat/sq", "squash", testutil.BaseTime.Add(2*time.Hour))
	}
	rebase := func(fx *branchFixture) {
		fx.repo.RebaseMerge("feat/sq", testutil.BaseTime.Add(2*time.Hour))
	}
	for _, tc := range []struct {
		name     string
		merge    func(fx *branchFixture)
		detector string
		verified string
	}{
		{"squash", squash, "merged-branch", "squash"},
		{"squash with forged claim", squash, "stale-branch", "in-remote"},
		{"rebase", rebase, "merged-branch", "squash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newBranchFixture(t)
			fx.featureBranch("feat/sq")
			tc.merge(fx)
			fx.repo.Push("main")
			f := fx.finding("feat/sq", tc.detector, tc.verified)

			_, err := fx.plan(f)
			wantBranchSkip(t, err, "--force")
			if r := skipReason(err); !strings.Contains(r, "no remote has the commits") || strings.Contains(r, "))") {
				t.Fatalf("reason = %q", r)
			}
			if !fx.branchExists("feat/sq") {
				t.Fatal("branch deleted without --force")
			}

			fx.env.Force = true
			step, en := fx.mustApply(f)
			if step.Command != "git branch -D -- feat/sq" || en.Status != session.StatusApplied {
				t.Fatalf("step = %+v, entry = %+v", step, en)
			}
			if fx.branchExists("feat/sq") {
				t.Fatal("branch still exists after forced apply")
			}
			if en.Undo["sha"] != f.Meta["tip"] {
				t.Fatalf("undo sha = %q, want the tip", en.Undo["sha"])
			}
		})
	}
}

// TestDeleteBranchAncestorMergedNeedsNoForce keeps the other side of the
// policy: a real merge (ancestry) is a fact, so an unpushed branch merged into
// the base is deleted without --force.
func TestDeleteBranchAncestorMergedNeedsNoForce(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/ff")
	fx.repo.Git("merge", "-q", "--ff-only", "feat/ff")
	step, en := fx.mustApply(fx.finding("feat/ff", "merged-branch", ""))
	if step.Command != "git branch -d -- feat/ff" || en.Status != session.StatusApplied {
		t.Fatalf("step = %+v, entry = %+v", step, en)
	}
}
