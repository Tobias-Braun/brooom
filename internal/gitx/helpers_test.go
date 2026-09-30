package gitx_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// at returns BaseTime shifted by n hours so commits get distinct, ordered dates.
func at(n int) time.Time { return testutil.BaseTime.Add(time.Duration(n) * time.Hour) }

// execRunner returns a real runner isolated from the developer's git config.
func execRunner(t *testing.T) *gitx.ExecRunner {
	t.Helper()
	r, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	home := testutil.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", home+"/.gitconfig-none")
	return r
}

// requireGit skips the test when the installed git is older than major.minor.
func requireGit(t *testing.T, r gitx.Runner, major, minor int) {
	t.Helper()
	v, err := gitx.GitVersion(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !v.AtLeast(major, minor) {
		t.Skipf("needs git %d.%d, have %s", major, minor, v)
	}
}

// openRepo opens an uncached handle on dir.
func openRepo(t *testing.T, r gitx.Runner, dir string) *gitx.Repo {
	t.Helper()
	repo, err := gitx.Open(context.Background(), r, dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// cachedRepo returns the memoizing handle from a fresh Cache.
func cachedRepo(t *testing.T, r gitx.Runner, dir string) *gitx.Repo {
	t.Helper()
	repo, err := gitx.NewCache(r).Repo(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// countRunner counts git invocations to prove memoization.
type countRunner struct {
	inner gitx.Runner
	n     atomic.Int64
}

func (c *countRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	c.n.Add(1)
	return c.inner.Run(ctx, dir, args...)
}

// fakeRunner answers git calls from a function, for parser and fallback tests.
type fakeRunner func(args []string) (string, error)

func (f fakeRunner) Run(_ context.Context, _ string, args ...string) (string, error) {
	return f(args)
}
