package worktrees_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/detectors/worktrees"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// activeHarness scans at the real clock so that the real mtimes of freshly
// created worktrees are "minutes old", like the checkout of a running agent.
func activeHarness(t *testing.T, repo *testutil.Repo, wt string) *harness {
	t.Helper()
	h := wtHarness(t, repo, wt)
	h.env.Now = time.Now()
	h.env.CacheDir = t.TempDir()
	h.env.Config.Thresholds.RecentDays = 2
	worktrees.SetOpenFiles(t, func(context.Context, []string) (map[string]bool, error) { return nil, nil })
	return h
}

// TestRecentWorktreeLowersConfidence covers issue #139: a worktree created
// or modified minutes ago (an agent still working) whose branch tip equals
// the base is "merged", but must not stay high confidence, which is what the
// safe preset acts on.
func TestRecentWorktreeLowersConfidence(t *testing.T) {
	tests := []struct {
		name     string
		branch   string
		age      time.Duration
		wantConf findings.Confidence
		recent   bool
	}{
		{"fresh checkout", "feat-fresh", 0, findings.ConfidenceMedium, true},
		{"modified an hour ago", "feat-hour", time.Hour, findings.ConfidenceMedium, true},
		{"untouched for a month", "feat-old", 30 * 24 * time.Hour, findings.ConfidenceHigh, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			wt := repo.AddWorktree("wt", tt.branch)
			h := activeHarness(t, repo, wt)
			if tt.age > 0 {
				ageTree(t, wt, time.Now().Add(-tt.age))
			}
			f := one(t, h.detect())
			if f.Confidence != tt.wantConf {
				t.Errorf("confidence %q, want %q", f.Confidence, tt.wantConf)
			}
			if got := f.HasRisk(findings.RiskRecentlyModified); got != tt.recent {
				t.Errorf("recently_modified = %v, want %v (flags %v)", got, tt.recent, f.RiskFlags)
			}
			if tt.recent && !slices.Contains(codes(f), "recently_modified") {
				t.Errorf("evidence lacks recently_modified: %v", codes(f))
			}
			// The flag is informational: it lowers confidence, it does not block.
			if f.SuggestedAction.Type != findings.ActionRemoveWorktree {
				t.Errorf("action %q", f.SuggestedAction.Type)
			}
		})
	}
}

// TestRecentDaysZeroDisablesRecency keeps the documented meaning of
// thresholds.recent_days = 0.
func TestRecentDaysZeroDisablesRecency(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("wt", "feat")
	h := activeHarness(t, repo, wt)
	h.env.Config.Thresholds.RecentDays = 0
	if f := one(t, h.detect()); f.Confidence != findings.ConfidenceHigh || f.HasRisk(findings.RiskRecentlyModified) {
		t.Errorf("got %q %v", f.Confidence, f.RiskFlags)
	}
}

// TestRecentUsesFreshMtimes: the scan cache keys on directory mtimes, and an
// in-place write does not change them. A worktree cached as old must still be
// seen as active once a file inside it is touched (issue #96 background).
func TestRecentUsesFreshMtimes(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("wt", "feat")
	h := activeHarness(t, repo, wt)
	old := time.Now().Add(-30 * 24 * time.Hour)
	ageTree(t, wt, old)
	if f := one(t, h.detect()); f.HasRisk(findings.RiskRecentlyModified) {
		t.Fatalf("aged worktree flagged recent: %v", f.RiskFlags)
	}

	// Touch a file only: the directory mtimes stay old, so a cached size
	// record would still claim the tree is old.
	file := filepath.Join(wt, "README.md")
	if _, err := os.Stat(file); err != nil {
		t.Skipf("no tracked file to touch: %v", err)
	}
	now := time.Now()
	if err := os.Chtimes(file, now, now); err != nil {
		t.Fatal(err)
	}
	if f := one(t, h.detect()); !f.HasRisk(findings.RiskRecentlyModified) {
		t.Errorf("touched worktree not flagged recent: %v", f.RiskFlags)
	}
}

// TestInUseWorktreeIsBlocked covers issues #100 and #139: a worktree a
// process has open (or stands in, which procs reports) is reported without an
// action, and --force does not lift that.
func TestInUseWorktreeIsBlocked(t *testing.T) {
	for _, force := range []bool{false, true} {
		repo := testutil.NewRepo(t)
		wt := repo.AddWorktree("wt", "feat")
		h := activeHarness(t, repo, wt)
		h.env.Force = force
		worktrees.SetOpenFiles(t, func(_ context.Context, paths []string) (map[string]bool, error) {
			return map[string]bool{paths[0]: true}, nil
		})
		f := one(t, h.detect())
		if !f.HasRisk(findings.RiskFileOpen) || f.SuggestedAction.Type != findings.ActionNone {
			t.Errorf("force=%v: flags %v action %q", force, f.RiskFlags, f.SuggestedAction.Type)
		}
		if !slices.Contains(codes(f), "worktree_in_use") || f.SuggestedAction.Reason == "" {
			t.Errorf("force=%v: evidence %v reason %q", force, codes(f), f.SuggestedAction.Reason)
		}
	}
}

// TestCwdInsideWorktreeIsBlocked: the shell of the user stands inside a merged
// worktree (a scan of the whole workspace from there). The open-file check
// says nothing, yet the worktree must not be offered for removal.
func TestCwdInsideWorktreeIsBlocked(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("wt", "feat")
	h := activeHarness(t, repo, wt)
	sub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	f := one(t, h.detect())
	if !f.HasRisk(findings.RiskFileOpen) || f.SuggestedAction.Type != findings.ActionNone {
		t.Errorf("flags %v action %q", f.RiskFlags, f.SuggestedAction.Type)
	}
	if !slices.Contains(codes(f), "worktree_in_use") {
		t.Errorf("evidence %v", codes(f))
	}
}

// TestUnknownOpenCheckIsNotSafe: an unavailable check keeps the suggestion
// (as the trash action does) but is visible as evidence.
func TestUnknownOpenCheckIsNotSafe(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("wt", "feat")
	h := activeHarness(t, repo, wt)
	worktrees.SetOpenFiles(t, func(context.Context, []string) (map[string]bool, error) { return nil, procs.ErrUnavailable })
	f := one(t, h.detect())
	if f.HasRisk(findings.RiskFileOpen) || f.SuggestedAction.Type != findings.ActionRemoveWorktree {
		t.Errorf("flags %v action %q", f.RiskFlags, f.SuggestedAction.Type)
	}
	if !slices.Contains(codes(f), "open_check_unavailable") {
		t.Errorf("evidence %v", codes(f))
	}
}

// TestSafePresetFloorExcludesActiveWorktrees: the safe preset only acts on
// high confidence findings, and an active worktree never reaches it.
func TestSafePresetFloorExcludesActiveWorktrees(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("wt", "feat")
	h := activeHarness(t, repo, wt)
	f := one(t, h.detect())
	if f.Confidence.Rank() >= findings.ConfidenceHigh.Rank() {
		t.Errorf("active worktree has confidence %q", f.Confidence)
	}
}
