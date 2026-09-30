package buildartifacts

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestInPlaceEditIsSeenWithWarmCache guards the walk contract: a cached
// NewestModTime is only a lower bound, and an in-place write to an existing
// file leaves every directory mtime untouched, so a non-Fresh size pass would
// keep reporting the old age and no recently_modified flag.
func TestInPlaceEditIsSeenWithWarmCache(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/a/index.js")
	f.settle(f.daysAgo(100))
	env := f.env()
	env.CacheDir = filepath.Join(t.TempDir(), "cache")

	warm, err := f.runWith(context.Background(), env, f.target())
	if err != nil || len(warm) != 1 || warm[0].AgeDays != 100 {
		t.Fatalf("warm-up run: %+v, err %v", warm, err)
	}

	testutil.WriteFile(t, f.dir, "node_modules/a/index.js", "y")
	f.touch("node_modules/a/index.js", f.daysAgo(1))
	f.touch("node_modules/a", f.daysAgo(100))
	f.touch("node_modules", f.daysAgo(100))

	got, err := f.runWith(context.Background(), env, f.target())
	if err != nil || len(got) != 1 {
		t.Fatalf("second run: %+v, err %v", got, err)
	}
	if got[0].AgeDays != 1 {
		t.Errorf("age = %d days, want 1 (stale cached mtime)", got[0].AgeDays)
	}
	if !slices.Contains(got[0].RiskFlags, findings.RiskRecentlyModified) {
		t.Errorf("flags = %v, want recently_modified", got[0].RiskFlags)
	}
}
