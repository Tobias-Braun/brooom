package worktrees_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// editInPlace rewrites an existing file and sets its mtime. Directory mtimes
// stay untouched, which is exactly what a cached directory record cannot
// notice and only a Fresh walk sees.
func editInPlace(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.SetMTime(t, path, when)
}

// TestInPlaceEditOfIgnoredFileKeepsWorktreeAlive: git status does not list
// ignored files, so IsDirty cannot catch an edit of one; only the newest
// mtime of a Fresh walk keeps the abandoned-checkout rule from firing.
func TestInPlaceEditOfIgnoredFileKeepsWorktreeAlive(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("stale", "feat-stale")
	testutil.WriteFile(t, wt, ".gitignore", "cache.bin\n")
	commitIn(t, repo, wt, "old.txt")
	ignored := testutil.WriteFile(t, wt, "cache.bin", "old")
	ageTree(t, wt, testutil.BaseTime)
	h := wtHarness(t, repo, wt)
	h.env.CacheDir = filepath.Join(t.TempDir(), "cache")

	// The warm-up run fills the cache and must see the abandoned checkout.
	one(t, h.detect())

	editInPlace(t, ignored, now.AddDate(0, 0, -1))
	if fs := h.detect(); len(fs) != 0 {
		t.Fatalf("worktree with a file edited yesterday reported as stale: %+v", fs)
	}
}

func TestRecentlyModifiedFlag(t *testing.T) {
	tests := []struct {
		name string
		edit time.Time
		want bool
	}{
		{"edited yesterday", now.AddDate(0, 0, -1), true},
		{"untouched", testutil.BaseTime, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			repo.WriteFile(".gitignore", "cache.bin\n")
			repo.CommitAll("ignore cache", testutil.BaseTime)
			wt := repo.AddWorktree("merged", "feat-merged")
			ignored := testutil.WriteFile(t, wt, "cache.bin", "old")
			ageTree(t, wt, testutil.BaseTime)
			h := wtHarness(t, repo, wt)
			h.env.CacheDir = filepath.Join(t.TempDir(), "cache")
			one(t, h.detect()) // warm the cache

			editInPlace(t, ignored, tt.edit)
			f := one(t, h.detect())
			if got := slices.Contains(f.RiskFlags, findings.RiskRecentlyModified); got != tt.want {
				t.Errorf("recently_modified = %v, want %v (flags %v)", got, tt.want, f.RiskFlags)
			}
			if f.SuggestedAction.Type != findings.ActionRemoveWorktree {
				t.Errorf("the informational flag must not block: action %q", f.SuggestedAction.Type)
			}
			if tt.want && f.AgeDays != 1 {
				t.Errorf("age = %d, want 1", f.AgeDays)
			}
		})
	}
}
