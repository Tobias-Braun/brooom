package gitx_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// versionCounter counts `git version` invocations.
type versionCounter struct {
	inner    gitx.Runner
	versions atomic.Int64
}

func (v *versionCounter) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if args[0] == "version" {
		v.versions.Add(1)
	}
	return v.inner.Run(ctx, dir, args...)
}

// TestCacheResolvesGitVersionOnce: the version used to be memoized per Repo
// handle and resolved under the cache-wide lock, so N repositories cost N
// `git version` runs.
func TestCacheResolvesGitVersionOnce(t *testing.T) {
	ctx := context.Background()
	counter := &versionCounter{inner: execRunner(t)}
	cache := gitx.NewCache(counter)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		repo := testutil.NewRepo(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Repo(ctx, repo.Dir); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := counter.versions.Load(); n != 1 {
		t.Errorf("git version ran %d times for 5 repositories, want 1", n)
	}
}

// TestCacheVersionPreSeedsRepo: a handle from the cache answers version
// queries (used by feature checks) without another process.
func TestCacheVersionPreSeedsRepo(t *testing.T) {
	ctx := context.Background()
	counter := &versionCounter{inner: execRunner(t)}
	cache := gitx.NewCache(counter)
	for i := 0; i < 2; i++ {
		repo, err := cache.Repo(ctx, testutil.NewRepo(t).Dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ListBranches(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := counter.versions.Load(); n != 1 {
		t.Errorf("git version ran %d times, want 1", n)
	}
}

// TestCacheVersionFailureIsNotMemoized: a cancelled context must not poison
// the scan; the next lookup asks git again.
func TestCacheVersionFailureIsNotMemoized(t *testing.T) {
	repo := testutil.NewRepo(t)
	cache := gitx.NewCache(&versionCounter{inner: execRunner(t)})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.Repo(cancelled, repo.Dir); err == nil {
		t.Fatal("cancelled context must fail")
	}
	if _, err := cache.Repo(context.Background(), repo.Dir); err != nil {
		t.Fatalf("later lookup must succeed, got %v", err)
	}
}
