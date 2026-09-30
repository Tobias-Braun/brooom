package logs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestScansShareOneOpenFileSnapshot is the regression test for one lsof run
// per target: two scans on one Env load the listing once, the per-call
// check is never used, and an open file in the snapshot is still flagged.
func TestScansShareOneOpenFileSnapshot(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	p := repo.WriteFile("npm-debug.log", "x\n")
	repo.WriteFile("README.md", "x\n")
	repo.CommitAll("init", testutil.BaseTime)
	testutil.SetMTime(t, p, daysAgo(100))
	oldDirs(t, repo.Dir)

	fakeOpenFiles(t, func(context.Context, []string) (map[string]bool, error) {
		t.Error("per-call open check used although the Env has a snapshot")
		return nil, nil
	})
	loads := 0
	env := newEnv(t, cfgDefault(), repo.Dir)
	env.Open = procs.NewSnapshotFrom(func(context.Context) ([]string, error) {
		loads++
		return []string{filepath.Clean(p)}, nil
	})

	for range 2 {
		got := byRel(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir, "npm-debug.log")
		if !hasFlag(got, findings.RiskFileOpen) {
			t.Fatalf("flags = %v, want file-open from the snapshot", got.RiskFlags)
		}
	}
	if loads != 1 {
		t.Errorf("listing loaded %d times, want 1", loads)
	}
}
