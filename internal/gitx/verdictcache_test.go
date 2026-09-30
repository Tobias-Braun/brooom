package gitx_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// freshBranches creates n unmerged branches that all sit one commit ahead of
// the current main tip, the shape parallel agent work leaves behind.
func freshBranches(t *testing.T, n int) (*testutil.Repo, []string) {
	t.Helper()
	repo := testutil.NewRepo(t)
	var names []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("agent/a%02d", i)
		repo.Git("checkout", "-q", "-b", name)
		repo.Commit(fmt.Sprintf("a%02d.txt", i), "x\n", "work "+name, at(i+1))
		repo.Checkout("main")
		names = append(names, name)
	}
	return repo, names
}

// TestSquashCheckOfFreshBranchesIsBatched is the exec-count regression test:
// an unmerged branch that already contains all of base cost a merge-base and a
// rev-list process each (more with a moved base). The batched ahead/behind
// counts answer all of them with one process.
func TestSquashCheckOfFreshBranchesIsBatched(t *testing.T) {
	requireGit(t, execRunner(t), 2, 41)
	ctx := context.Background()
	const n = 40
	repo, names := freshBranches(t, n)
	spy := &spyRunner{inner: execRunner(t)}
	handle := cachedRepo(t, spy, repo.Dir)
	if _, err := handle.ListBranches(ctx); err != nil {
		t.Fatal(err)
	}
	spy.reset()
	for _, name := range names {
		res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/"+name, true)
		if err != nil || res.Merged {
			t.Fatalf("%s: %+v, %v; want not merged", name, res, err)
		}
	}
	if limit := 8; spy.calls > limit {
		t.Errorf("%d git processes for %d unmerged branches, want at most %d", spy.calls, n, limit)
	}
}

// TestBehindShortcutKeepsAnswers: the shortcut must not change any verdict
// when base did move: a squash-merged branch is still found, and a branch that
// sits on the base tip is still reported by the ancestor check.
func TestBehindShortcutKeepsAnswers(t *testing.T) {
	requireGit(t, execRunner(t), 2, 41)
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	featWithTwoCommits(repo)
	repo.SquashMerge("feat", "squash feat", at(3))
	repo.Git("branch", "fresh", "main")
	handle := cachedRepo(t, execRunner(t), repo.Dir)
	if res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/feat", true); err != nil || res.Method != gitx.MethodSquash {
		t.Errorf("feat: %+v, %v; want squash", res, err)
	}
	if res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/fresh", true); err != nil || res.Method != gitx.MethodAncestor {
		t.Errorf("fresh: %+v, %v; want ancestor", res, err)
	}
}

func TestParseBehind(t *testing.T) {
	out := "refs/heads/a\x000 3\nrefs/heads/b\x002 0\nrefs/heads/bad\x00x y\nrefs/heads/none\x00\nnonsense\n"
	got := gitx.ParseBehind(out)
	if len(got) != 2 || got["refs/heads/a"] != 3 || got["refs/heads/b"] != 0 {
		t.Errorf("ParseBehind = %v", got)
	}
}

// cmdSpy records the git subcommands that run.
type cmdSpy struct {
	inner gitx.Runner
	mu    sync.Mutex
	cmds  []string
}

func (c *cmdSpy) Run(ctx context.Context, dir string, args ...string) (string, error) {
	c.mu.Lock()
	c.cmds = append(c.cmds, args[0])
	c.mu.Unlock()
	return c.inner.Run(ctx, dir, args...)
}

// RunInput records stdin-fed commands (patch-id) too; the wrapped runner is
// the real one, so patch-id needs input support.
func (c *cmdSpy) RunInput(ctx context.Context, dir string, in io.Reader, args ...string) (string, error) {
	c.mu.Lock()
	c.cmds = append(c.cmds, args[0])
	c.mu.Unlock()
	return c.inner.(*gitx.ExecRunner).RunInput(ctx, dir, in, args...)
}

// count returns how often subcommand ran.
func (c *cmdSpy) count(sub string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, s := range c.cmds {
		if s == sub {
			n++
		}
	}
	return n
}

// verdictRepo has a squash-merged branch "feat" and an unmerged "other" on a
// moved base, so both verdicts need the patch-id path.
func verdictRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	featWithTwoCommits(repo)
	repo.SquashMerge("feat", "squash feat", at(3))
	repo.Commit("later.txt", "l\n", "unrelated", at(4))
	repo.Git("checkout", "-q", "-b", "other", "HEAD~2")
	repo.Commit("o.txt", "o\n", "other work", at(5))
	repo.Checkout("main")
	return repo
}

// scanVerdicts runs one scan-like pass with the verdict cache in dir and
// returns the spy of that pass.
func scanVerdicts(t *testing.T, repo *testutil.Repo, dir string) *cmdSpy {
	t.Helper()
	ctx := context.Background()
	spy := &cmdSpy{inner: execRunner(t)}
	cache := gitx.NewCache(spy)
	cache.SetVerdictDir(dir)
	handle, err := cache.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/feat", true); err != nil || res.Method != gitx.MethodSquash {
		t.Fatalf("feat: %+v, %v; want squash", res, err)
	}
	if res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/other", true); err != nil || res.Merged {
		t.Fatalf("other: %+v, %v; want not merged", res, err)
	}
	return spy
}

// TestVerdictCacheServesLaterScans: the second scan of unchanged refs computes
// no patch id, in either direction (merged and not merged).
func TestVerdictCacheServesLaterScans(t *testing.T) {
	repo := verdictRepo(t)
	dir := filepath.Join(testutil.ResolvedTempDir(t), "verdicts")
	first := scanVerdicts(t, repo, dir)
	if first.count("diff") == 0 && first.count("log") == 0 {
		t.Fatal("first scan should have computed patch ids")
	}
	second := scanVerdicts(t, repo, dir)
	for _, sub := range []string{"diff", "log", "merge-base", "rev-list"} {
		if n := second.count(sub); n != 0 {
			t.Errorf("second scan ran git %s %d times, want 0", sub, n)
		}
	}
}

// TestVerdictCacheIsKeyedByTips: moving the branch tip must not reuse the old
// verdict.
func TestVerdictCacheIsKeyedByTips(t *testing.T) {
	ctx := context.Background()
	repo := verdictRepo(t)
	dir := filepath.Join(testutil.ResolvedTempDir(t), "verdicts")
	scanVerdicts(t, repo, dir)
	// Rebuild "other" so that it becomes a squash of main's later commit.
	repo.Checkout("other")
	repo.Git("reset", "-q", "--hard", "main")
	repo.Checkout("main")
	cache := gitx.NewCache(execRunner(t))
	cache.SetVerdictDir(dir)
	handle, err := cache.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/other", true); err != nil || res.Method != gitx.MethodAncestor {
		t.Errorf("other after reset: %+v, %v; want ancestor", res, err)
	}
}

// TestVerdictCacheNeverStoresTruncated: a capped check is unknown, not a
// verdict, and a later scan with a bigger cap must compute it again.
func TestVerdictCacheNeverStoresTruncated(t *testing.T) {
	ctx := context.Background()
	repo := verdictRepo(t)
	dir := filepath.Join(testutil.ResolvedTempDir(t), "verdicts")
	cache := gitx.NewCache(execRunner(t))
	cache.SetVerdictDir(dir)
	handle, err := cache.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	gitx.SetDiffLimit(handle, 10)
	res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/feat", true)
	if err != nil || res.Merged || !res.Truncated {
		t.Fatalf("feat with tiny cap: %+v, %v; want truncated", res, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("truncated verdict was stored: %d files", len(entries))
	}
}

// TestVerdictCacheNeverStoresErrors: an unresolvable ref is an error, not a
// verdict.
func TestVerdictCacheNeverStoresErrors(t *testing.T) {
	ctx := context.Background()
	repo := verdictRepo(t)
	dir := filepath.Join(testutil.ResolvedTempDir(t), "verdicts")
	cache := gitx.NewCache(execRunner(t))
	cache.SetVerdictDir(dir)
	handle, err := cache.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/does-not-exist", true); err == nil {
		t.Fatal("want an error for a missing branch")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("error result was stored: %d files", len(entries))
	}
}

// TestVerdictCacheIgnoresDamagedFiles: unreadable, foreign or inconsistent
// files are misses; the verdict is recomputed and corrected.
func TestVerdictCacheIgnoresDamagedFiles(t *testing.T) {
	repo := verdictRepo(t)
	dir := filepath.Join(testutil.ResolvedTempDir(t), "verdicts")
	scanVerdicts(t, repo, dir)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("want 2 verdict files, got %d, %v", len(entries), err)
	}
	damage := []string{
		"not json",
		`{"version":"0","merged":true,"method":"squash"}`,
		`{"version":"1","merged":true,"method":"ancestor"}`,
		`{"version":"1","merged":false,"method":"squash"}`,
	}
	for i, content := range damage {
		for _, e := range entries {
			if err := os.WriteFile(filepath.Join(dir, e.Name()), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// scanVerdicts asserts the correct verdicts, so a trusted bad file fails it.
		if spy := scanVerdicts(t, repo, dir); spy.count("diff")+spy.count("log") == 0 {
			t.Errorf("damage case %d was trusted instead of recomputed", i)
		}
	}
}

// TestVerdictCacheOffByDefault: without a directory nothing is written.
func TestVerdictCacheOffByDefault(t *testing.T) {
	repo := verdictRepo(t)
	first := scanVerdicts(t, repo, "")
	second := scanVerdicts(t, repo, "")
	if first.count("diff") != second.count("diff") || second.count("diff")+second.count("log") == 0 {
		t.Errorf("without a cache every scan recomputes: first %d, second %d diffs", first.count("diff"), second.count("diff"))
	}
}

// TestUncachedHandleNeverReadsVerdicts: actions open uncached handles and must
// verify against the live repository even when a forged verdict exists.
func TestUncachedHandleNeverReadsVerdicts(t *testing.T) {
	ctx := context.Background()
	repo := verdictRepo(t)
	dir := filepath.Join(testutil.ResolvedTempDir(t), "verdicts")
	scanVerdicts(t, repo, dir)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		forged := `{"version":"1","merged":true,"method":"squash"}`
		if err := os.WriteFile(filepath.Join(dir, e.Name()), []byte(forged), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handle := openRepo(t, execRunner(t), repo.Dir)
	if res, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/other", true); err != nil || res.Merged {
		t.Errorf("uncached handle answered %+v, %v; want not merged", res, err)
	}
}
