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
func activeHarness(t *testing.T, repo *testutil.Repo, wts ...string) *harness {
	t.Helper()
	h := wtHarness(t, repo, wts...)
	h.env.Now = time.Now()
	h.env.CacheDir = t.TempDir()
	h.env.Config.Thresholds.RecentDays = 2
	worktrees.SetOpenFiles(t, func(context.Context, []string) (map[string]bool, error) { return nil, nil })
	return h
}

// TestRecentWorktreeIsOnlyFlagged covers issue #255: a worktree created or
// modified minutes ago (left by an agent run) is flagged recently_modified
// for information but keeps its high confidence and its removal action, so
// the cleanup can run right after a large agent workflow.
func TestRecentWorktreeIsOnlyFlagged(t *testing.T) {
	tests := []struct {
		name     string
		branch   string
		age      time.Duration
		wantConf findings.Confidence
		recent   bool
	}{
		{"fresh checkout", "feat-fresh", 0, findings.ConfidenceHigh, true},
		{"modified an hour ago", "feat-hour", time.Hour, findings.ConfidenceHigh, true},
		{"untouched for a month", "feat-old", 30 * 24 * time.Hour, findings.ConfidenceHigh, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			wt := repo.AddStartedWorktree("wt", tt.branch)
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
			// The flag is informational: neither confidence nor action change.
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
	wt := repo.AddStartedWorktree("wt", "feat")
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
	wt := repo.AddStartedWorktree("wt", "feat")
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
		wt := repo.AddStartedWorktree("wt", "feat")
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

// TestOpenCheckIsBatchedPerScan: three candidate worktrees are checked with
// one open-file call, not one call each, and the answer still lands on the
// right worktree. That the call then costs one lsof run on macOS is pinned in
// internal/procs (TestLsofDirectoriesShareOneRun).
func TestOpenCheckIsBatchedPerScan(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt1 := repo.AddStartedWorktree("wt1", "feat1")
	wt2 := repo.AddStartedWorktree("wt2", "feat2")
	wt3 := repo.AddStartedWorktree("wt3", "feat3")
	h := activeHarness(t, repo, wt1, wt2, wt3)
	var calls, checked int
	worktrees.SetOpenFiles(t, func(_ context.Context, paths []string) (map[string]bool, error) {
		calls++
		checked = len(paths)
		return map[string]bool{paths[0]: true}, nil
	})
	got := h.detect()
	if len(got) != 3 {
		t.Fatalf("got %d findings, want 3", len(got))
	}
	if calls != 1 || checked != 3 {
		t.Errorf("open-file check ran %d times over %d paths, want 1 over 3", calls, checked)
	}
	inUse := 0
	for _, f := range got {
		if f.HasRisk(findings.RiskFileOpen) {
			inUse++
		}
	}
	if inUse != 1 {
		t.Errorf("%d worktrees flagged in use, want exactly 1", inUse)
	}
}

// TestCwdInsideWorktreeIsBlocked: the shell of the user stands inside a merged
// worktree (a scan of the whole workspace from there). The open-file check
// says nothing, yet the worktree must not be offered for removal.
func TestCwdInsideWorktreeIsBlocked(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddStartedWorktree("wt", "feat")
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
	wt := repo.AddStartedWorktree("wt", "feat")
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

// TestSafePresetFloorIncludesFreshWorktrees covers issue #255: a clean merged
// worktree left by an agent run reaches the high confidence the safe preset
// acts on, however young it is.
func TestSafePresetFloorIncludesFreshWorktrees(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddStartedWorktree("wt", "feat")
	h := activeHarness(t, repo, wt)
	f := one(t, h.detect())
	if f.Confidence != findings.ConfidenceHigh {
		t.Errorf("fresh merged worktree has confidence %q, want high", f.Confidence)
	}
}
