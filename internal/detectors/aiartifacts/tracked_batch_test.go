package aiartifacts

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// lsFilesCounter counts the `git ls-files` processes a scan starts.
type lsFilesCounter struct {
	gitx.Runner
	n atomic.Int64
}

func (c *lsFilesCounter) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "ls-files" {
		c.n.Add(1)
	}
	return c.Runner.Run(ctx, dir, args...)
}

// TestScanAsksGitForTrackedFilesOnce is the regression test for #217: the
// tracked check used to run one `git ls-files` per candidate, so the process
// count grew with the number of findings (and each call with the index size).
func TestScanAsksGitForTrackedFilesOnce(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	const n = 40
	repo.WriteFile("README.md", "x")
	repo.CommitAll("init", testutil.BaseTime)
	for i := 0; i < n; i++ {
		put(t, repo.Dir, fmt.Sprintf("pkg%d/.aider.chat.history.md", i), 100)
	}
	// One of them is tracked and must still be reported as such.
	repo.Git("add", "pkg7/.aider.chat.history.md")
	repo.Git("commit", "-q", "-m", "track log")
	oldDirs(t, repo.Dir)
	env := newEnv(t, cfgDefault(), repo.Dir)
	counter := &lsFilesCounter{Runner: env.Git}
	env.Git = counter
	env.Repos = gitx.NewCache(counter)

	got := mustScan(t, env, repoTarget(repo.Dir))
	if len(got) < n {
		t.Fatalf("scan found %d findings, want at least %d", len(got), n)
	}
	if calls := counter.n.Load(); calls != 1 {
		t.Errorf("git ls-files ran %d times for %d findings, want 1", calls, len(got))
	}
	if f := byRel(t, got, repo.Dir, "pkg7/.aider.chat.history.md"); !hasFlag(f, findings.RiskTrackedFiles) {
		t.Errorf("tracked file lost its flag: %+v", f)
	}
	if f := byRel(t, got, repo.Dir, "pkg8/.aider.chat.history.md"); hasFlag(f, findings.RiskTrackedFiles) {
		t.Errorf("untracked file wrongly flagged: %+v", f)
	}
}
