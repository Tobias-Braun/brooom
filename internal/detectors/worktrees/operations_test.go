package worktrees_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// adminDir returns the private git directory of the linked worktree wt.
func adminDir(t *testing.T, repo *testutil.Repo, wt string) string {
	t.Helper()
	return filepath.Clean(strings.TrimSpace(repo.Git("-C", wt, "rev-parse", "--absolute-git-dir")))
}

// TestOperationInProgressBlocksRemoval covers a merged worktree paused in a
// rebase, merge, cherry-pick, revert or bisect: removing it would destroy the
// operation state, so the removal must not be suggested and --force must not
// lift that.
func TestOperationInProgressBlocksRemoval(t *testing.T) {
	tests := []struct {
		name   string
		marker string
		isDir  bool
	}{
		{"interactive rebase", "rebase-merge", true},
		{"apply rebase", "rebase-apply", true},
		{"merge", "MERGE_HEAD", false},
		{"cherry-pick", "CHERRY_PICK_HEAD", false},
		{"revert", "REVERT_HEAD", false},
		{"bisect", "BISECT_LOG", false},
		{"sequencer", "sequencer", true},
	}
	for _, tt := range tests {
		for _, force := range []bool{false, true} {
			name := tt.name
			if force {
				name += " with force"
			}
			t.Run(name, func(t *testing.T) {
				repo := testutil.NewRepo(t)
				wt := repo.AddWorktree("paused", "")
				h := wtHarness(t, repo, wt)
				h.env.Force = force
				marker := filepath.Join(adminDir(t, repo, wt), tt.marker)
				if tt.isDir {
					if err := os.Mkdir(marker, 0o755); err != nil {
						t.Fatal(err)
					}
				} else {
					testutil.WriteFile(t, filepath.Dir(marker), tt.marker, "x\n")
				}

				f := one(t, h.detect())
				if f.SuggestedAction.Type != findings.ActionNone || f.SuggestedAction.Reason == "" {
					t.Errorf("action %+v", f.SuggestedAction)
				}
				if !f.HasRisk(findings.RiskWorktreeOperation) || findings.Actionable(f.RiskFlags, true) {
					t.Errorf("flags %v must block even with force", f.RiskFlags)
				}
				evidence(t, f, "worktree_operation_in_progress")
			})
		}
	}
}

// TestSubmoduleWorktreeIsReportedWithoutAction covers a merged, clean
// worktree with an initialized submodule: `git worktree remove` always
// refuses it, so no removal may be suggested.
func TestSubmoduleWorktreeIsReportedWithoutAction(t *testing.T) {
	sub := testutil.NewRepo(t)
	repo := testutil.NewRepo(t)
	repo.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "sub")
	repo.CommitAll("add submodule", testutil.BaseTime)
	wt := repo.AddWorktree("with-sub", "feat-sub")
	gitIn(t, repo, wt, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
	h := wtHarness(t, repo, wt)

	f := one(t, h.detect())
	if f.SuggestedAction.Type != findings.ActionNone || f.SuggestedAction.Reason == "" {
		t.Errorf("action %+v", f.SuggestedAction)
	}
	evidence(t, f, "worktree_has_submodules")
	evidence(t, f, "merged_into")
}
