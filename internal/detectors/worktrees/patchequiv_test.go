package worktrees_test

import (
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// detachedPreRebase leaves a detached worktree at the tip of a one-commit
// feature branch and deletes the branch, so no ref holds the commit. With
// landed the commit is rebase-merged into main under a new id first.
func detachedPreRebase(repo *testutil.Repo, landed bool) string {
	repo.Git("checkout", "-q", "-b", "feat-agent")
	repo.Commit("agent.txt", "agent", "agent work", testutil.BaseTime.Add(time.Hour))
	wt := repo.AddWorktree("det-agent", "")
	repo.Git("checkout", "-q", "main")
	if landed {
		repo.RebaseMerge("feat-agent", testutil.BaseTime.Add(2*time.Hour))
	}
	repo.Git("branch", "-D", "feat-agent")
	return wt
}

// TestDetachedPatchEquivalentIsRemovable covers issue #255: a detached HEAD
// whose commits all landed on the base under other ids counts as merged, is
// fresh (recent_days informational) and needs no --force, while a genuinely
// unique commit or disabled squash detection keeps it unreported.
func TestDetachedPatchEquivalentIsRemovable(t *testing.T) {
	tests := []struct {
		name   string
		landed bool
		mode   config.MergeMode
		want   bool
	}{
		{"rebased onto main", true, config.MergeAncestorSquash, true},
		{"unique commit", false, config.MergeAncestorSquash, false},
		{"squash detection off", true, config.MergeAncestor, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			wt := detachedPreRebase(repo, tt.landed)
			h := activeHarness(t, repo, wt)
			h.env.Config.Detectors.MergedBranch.Mode = tt.mode
			fs := h.detect()
			if !tt.want {
				if len(fs) != 0 {
					t.Fatalf("reported %+v", fs)
				}
				return
			}
			f := one(t, fs)
			if f.SuggestedAction.Type != findings.ActionRemoveWorktree || f.Confidence != findings.ConfidenceMedium {
				t.Errorf("action %q confidence %q", f.SuggestedAction.Type, f.Confidence)
			}
			if ev := evidence(t, f, "head_patch_equivalent"); ev.Value != "main" {
				t.Errorf("value %v", ev.Value)
			}
			if !findings.Actionable(f.RiskFlags, false) {
				t.Errorf("flags %v block without --force", f.RiskFlags)
			}
		})
	}
}
