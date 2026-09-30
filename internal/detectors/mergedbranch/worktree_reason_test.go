package mergedbranch_test

import (
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// TestCheckedOutReasonNamesWorktreeAndScopeHint covers the block reason of a
// branch checked out in a worktree: it always names the worktree and, when the
// worktree lies outside the guard, says how to bring it into scope.
func TestCheckedOutReasonNamesWorktreeAndScopeHint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inScope  bool
		wantHint bool
	}{
		{"outside the guard", false, true},
		{"inside the guard", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.feature("feat/wt", "w.txt")
			f.merge("feat/wt")
			f.publish()
			wt := f.repo.AddWorktree("wt", "feat/wt")
			if tc.inScope {
				guard, err := scope.NewGuard(f.repo.Dir, wt)
				if err != nil {
					t.Fatal(err)
				}
				f.env.Guard = guard
			}

			reason := mustFind(t, f.detect(), "feat/wt").SuggestedAction.Reason
			if !strings.Contains(reason, gitx.NormalizePath(wt)) {
				t.Errorf("reason does not name the worktree %s: %q", wt, reason)
			}
			if got := strings.Contains(reason, scope.OutsideWorktreeHint); got != tc.wantHint {
				t.Errorf("hint present = %v, want %v: %q", got, tc.wantHint, reason)
			}
			if !strings.Contains(reason, "--force does not override") {
				t.Errorf("reason lost the force note: %q", reason)
			}
		})
	}
}
