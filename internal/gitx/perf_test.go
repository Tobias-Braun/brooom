package gitx_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// spyRunner counts every git process (plain and stdin-fed) and the "commit "
// header lines of `log -p` output, which is the number of commits that were
// diffed.
type spyRunner struct {
	inner *gitx.ExecRunner
	mu    sync.Mutex
	calls int
	diffs int
}

func (s *spyRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := s.inner.Run(ctx, dir, args...)
	s.record(args, out)
	return out, err
}

func (s *spyRunner) RunInput(ctx context.Context, dir string, in io.Reader, args ...string) (string, error) {
	out, err := s.inner.RunInput(ctx, dir, in, args...)
	s.record(args, out)
	return out, err
}

func (s *spyRunner) record(args []string, out string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if args[0] != "log" {
		return
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "commit ") {
			s.diffs++
		}
	}
}

func (s *spyRunner) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls, s.diffs = 0, 0
}

// manyBranches builds a repository with n branches: the first third merged
// into main (ancestor), the rest unmerged and forked from the final main tip,
// as agent branches usually are. It returns the branch names.
func manyBranches(t *testing.T, n int) (*testutil.Repo, []string) {
	t.Helper()
	repo := testutil.NewRepo(t)
	var names []string
	for i := 0; i < n; i++ {
		merged := i < n/3
		name := fmt.Sprintf("feat/b%02d", i)
		repo.Git("checkout", "-q", "-b", name)
		repo.Commit(fmt.Sprintf("f%02d.txt", i), "x\n", "work "+name, at(i+1))
		repo.Checkout("main")
		if merged {
			repo.GitAt(at(i+1), "merge", "--no-ff", "-q", "-m", "merge "+name, name)
		}
		names = append(names, name)
	}
	return repo, names
}

// TestMergedIntoSpawnCount is the regression test for the ~9-10 git spawns per
// branch: 60 branches used to cost roughly 3 processes each (more with a moved base), one ancestor
// check per branch plus re-resolving refs and re-diffing base history.
func TestMergedIntoSpawnCount(t *testing.T) {
	ctx := context.Background()
	repo, names := manyBranches(t, 60)
	spy := &spyRunner{inner: execRunner(t)}
	cache := gitx.NewCache(spy)
	handle, err := cache.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ListBranches(ctx); err != nil {
		t.Fatal(err)
	}
	spy.reset()

	cachedResults := map[string]gitx.MergeResult{}
	for _, n := range names {
		res, err := handle.MergedInto(ctx, "main", n, true)
		if err != nil {
			t.Fatal(err)
		}
		cachedResults[n] = res
	}
	if spy.calls > 2*len(names) {
		t.Errorf("%d git processes for %d branches, want at most %d", spy.calls, len(names), 2*len(names))
	}

	// The reduction must not change any answer: compare with an uncached
	// handle, which asks git per branch.
	plain := openRepo(t, execRunner(t), repo.Dir)
	for _, n := range names {
		want, err := plain.MergedInto(ctx, "main", n, true)
		if err != nil {
			t.Fatal(err)
		}
		if cachedResults[n] != want {
			t.Errorf("%s: batched %+v, per-branch %+v", n, cachedResults[n], want)
		}
	}
}

// TestBaseCommitsAreDiffedOnce: branches forked from many different points of
// main used to re-diff the base history once per fork point (quadratic). Every
// commit must now be diffed at most once per scan.
func TestBaseCommitsAreDiffedOnce(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	const forks = 12
	var names []string
	for i := 0; i < forks; i++ {
		repo.Commit(fmt.Sprintf("m%02d.txt", i), "m\n", "main work", at(i))
		name := fmt.Sprintf("feat/f%02d", i)
		repo.Git("checkout", "-q", "-b", name)
		repo.Commit(fmt.Sprintf("b%02d.txt", i), "b\n", "branch work", at(100+i))
		repo.Checkout("main")
		names = append(names, name)
	}
	// One more base commit after every fork point.
	repo.Commit("last.txt", "l\n", "main work", at(50))

	spy := &spyRunner{inner: execRunner(t)}
	handle := cachedRepo(t, spy, repo.Dir)
	for _, n := range names {
		if res, err := handle.MergedInto(ctx, "main", n, true); err != nil || res.Merged {
			t.Fatalf("%s: %+v, %v; want not merged", n, res, err)
		}
	}
	// forks+1 base commits after the root, plus one commit per branch.
	if limit := (forks + 1) + forks; spy.diffs > limit {
		t.Errorf("%d commits diffed, want at most %d (each once)", spy.diffs, limit)
	}
}

// TestSquashMergedAfterBaseMoved keeps the detection itself honest with base
// commits diffed lazily: a squash merge followed by unrelated base commits
// and a second, unmerged branch.
func TestSquashMergedAfterBaseMoved(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	featWithTwoCommits(repo)
	repo.SquashMerge("feat", "squash feat", at(3))
	repo.Commit("later.txt", "l\n", "unrelated", at(4))
	repo.Git("checkout", "-q", "-b", "other", "HEAD~2")
	repo.Commit("o.txt", "o\n", "other work", at(5))
	repo.Checkout("main")

	handle := cachedRepo(t, execRunner(t), repo.Dir)
	if res, err := handle.MergedInto(ctx, "main", "feat", true); err != nil || res.Method != gitx.MethodSquash {
		t.Errorf("feat: %+v, %v; want squash", res, err)
	}
	if res, err := handle.MergedInto(ctx, "main", "other", true); err != nil || res.Merged {
		t.Errorf("other: %+v, %v; want not merged", res, err)
	}
}

// TestCacheRepoResolvesOnce: env.Repo runs for every detector and target; the
// three rev-parse calls used to run before the cache lookup every time.
func TestCacheRepoResolvesOnce(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	spy := &spyRunner{inner: execRunner(t)}
	cache := gitx.NewCache(spy)

	first, err := cache.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Errorf("first lookup used %d git processes, want 1 combined rev-parse", spy.calls)
	}
	for i := 0; i < 5; i++ {
		again, err := cache.Repo(ctx, repo.Dir)
		if err != nil || again != first {
			t.Fatalf("lookup %d: %p, %v; want the shared handle", i, again, err)
		}
	}
	if spy.calls != 1 {
		t.Errorf("repeated lookups spawned git: %d processes in total", spy.calls)
	}
}

func TestOpenRefusesBareAndNonRepos(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	bare := testutil.ResolvedTempDir(t)
	if _, err := r.Run(ctx, bare, "init", "-q", "--bare"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Open(ctx, r, bare); !errors.Is(err, gitx.ErrBareRepo) {
		t.Errorf("bare: err = %v, want ErrBareRepo", err)
	}
	if _, err := gitx.NewCache(r).Repo(ctx, testutil.ResolvedTempDir(t)); !errors.Is(err, gitx.ErrNotRepo) {
		t.Errorf("non-repo: err = %v, want ErrNotRepo", err)
	}
}
