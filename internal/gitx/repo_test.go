package gitx_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestPathsFromLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("linked", "feat")
	sub := filepath.Dir(testutil.WriteFile(t, repo.Dir, "a/b/c.txt", "x"))
	common, err := gitx.CommonDir(ctx, r, repo.Dir)
	if err != nil || !filepath.IsAbs(common) {
		t.Fatalf("CommonDir = %q, %v", common, err)
	}

	tests := []struct {
		name string
		fn   func(dir string) (string, error)
		dir  string
		want string
	}{
		{"TopLevel linked", func(d string) (string, error) { return gitx.TopLevel(ctx, r, d) }, wt, wt},
		{"TopLevel subdir", func(d string) (string, error) { return gitx.TopLevel(ctx, r, d) }, sub, repo.Dir},
		{"CommonDir linked", func(d string) (string, error) { return gitx.CommonDir(ctx, r, d) }, wt, common},
		{"CommonDir subdir", func(d string) (string, error) { return gitx.CommonDir(ctx, r, d) }, sub, common},
		{"MainWorktree main", func(d string) (string, error) { return gitx.MainWorktree(ctx, r, d) }, repo.Dir, repo.Dir},
		{"MainWorktree linked", func(d string) (string, error) { return gitx.MainWorktree(ctx, r, d) }, wt, repo.Dir},
	}
	for _, tc := range tests {
		got, err := tc.fn(tc.dir)
		if err != nil || got != tc.want {
			t.Errorf("%s = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	t.Run("not a repo", func(t *testing.T) {
		if _, err := gitx.Open(ctx, r, testutil.ResolvedTempDir(t)); !errors.Is(err, gitx.ErrNotRepo) {
			t.Fatalf("err = %v, want ErrNotRepo", err)
		}
	})
	t.Run("bare repo", func(t *testing.T) {
		origin := testutil.NewRepoWithRemote(t).Origin
		if _, err := gitx.Open(ctx, r, origin); !errors.Is(err, gitx.ErrBareRepo) {
			t.Fatalf("err = %v, want ErrBareRepo", err)
		}
		if _, err := gitx.NewCache(r).Repo(ctx, origin); !errors.Is(err, gitx.ErrBareRepo) {
			t.Fatalf("cache err = %v, want ErrBareRepo", err)
		}
	})
	t.Run("missing dir", func(t *testing.T) {
		if _, err := gitx.Open(ctx, r, filepath.Join(testutil.ResolvedTempDir(t), "gone")); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestNormalizePath(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	got := gitx.NormalizePath(dir + string(filepath.Separator) + "." + string(filepath.Separator))
	if got != dir {
		t.Errorf("NormalizePath = %q, want %q", got, dir)
	}
	missing := filepath.Join(dir, "missing", "..", "gone")
	if got := gitx.NormalizePath(missing); got != filepath.Join(dir, "gone") {
		t.Errorf("missing path not cleaned: %q", got)
	}
	if runtime.GOOS == "windows" {
		if got := gitx.NormalizePath("C:/does/not/exist"); got != `C:\does\not\exist` {
			t.Errorf("forward slashes not converted: %q", got)
		}
	}
}

func TestCacheSharesRepoPerCommonDir(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("linked", "feat")
	other := testutil.NewRepo(t)

	cache := gitx.NewCache(r)
	a, err := cache.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := cache.Repo(ctx, wt)
	c, _ := cache.Repo(ctx, other.Dir)
	if a != b {
		t.Error("main and linked worktree must share one handle")
	}
	if a == c {
		t.Error("different repositories must not share a handle")
	}
}

func TestUncachedHandleSeesLiveState(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	repo := testutil.NewRepo(t)
	uncached := openRepo(t, r, repo.Dir)
	cached := cachedRepo(t, r, repo.Dir)
	if _, err := uncached.ListBranches(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.ListBranches(ctx); err != nil {
		t.Fatal(err)
	}
	repo.Branch("late")
	got, _ := uncached.ListBranches(ctx)
	if len(got) != 2 {
		t.Errorf("uncached handle must see the new branch, got %d branches", len(got))
	}
	got, _ = cached.ListBranches(ctx)
	if len(got) != 1 {
		t.Errorf("cached handle must memoize, got %d branches", len(got))
	}
}

// TestRepoConcurrentUse hammers one shared handle; run with -race.
func TestRepoConcurrentUse(t *testing.T) {
	ctx := context.Background()
	cr := &countRunner{inner: execRunner(t)}
	repo := testutil.NewRepoWithRemote(t)
	repo.Branch("feat")
	shared := cachedRepo(t, cr, repo.Dir)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = shared.ListBranches(ctx)
			_, _ = shared.ListWorktrees(ctx)
			_, _ = shared.ListRemoteBranches(ctx)
			_, _ = shared.DefaultBase(ctx, []string{"main"})
			_, _ = shared.RemoteContaining(ctx, "main")
			_, _ = shared.MergedInto(ctx, "main", "feat", true)
			_ = shared.OpenPRBranches(ctx, repo.Dir, gitx.PROptions{GH: failingGH})
		}()
	}
	wg.Wait()

	before := cr.n.Load()
	_, _ = shared.ListBranches(ctx)
	_, _ = shared.MergedInto(ctx, "main", "feat", true)
	if cr.n.Load() != before {
		t.Error("memoized calls must not start git processes")
	}
}
