package scope

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// isolateHome points the home directory at a fresh temp dir so tests never
// depend on the developer's real home, and returns it.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := testutil.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// mkdir creates dir/rel (forward slashes).
func mkdir(t testing.TB, dir, rel string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeRepo creates a minimal repository (an empty .git directory with HEAD).
func fakeRepo(t testing.TB, dir, rel string) string {
	t.Helper()
	p := mkdir(t, dir, rel)
	testutil.WriteFile(t, p, ".git/HEAD", "ref: refs/heads/main\n")
	return p
}

// touch creates an empty file dir/rel.
func touch(t testing.TB, dir, rel string) {
	t.Helper()
	testutil.WriteFile(t, dir, rel, "")
}

// got is one discovered target in comparable form.
type got struct {
	rel  string
	kind TargetKind
}

// summarize maps targets to root-relative (forward slash) form.
func summarize(root string, ts []Target) []got {
	out := make([]got, 0, len(ts))
	for _, tg := range ts {
		rel, err := filepath.Rel(root, tg.Path)
		if err != nil {
			rel = tg.Path
		}
		out = append(out, got{filepath.ToSlash(rel), tg.Kind})
	}
	return out
}

// discoverOK runs Discover on one root and fails on error.
func discoverOK(t *testing.T, root string, opts DiscoverOptions) []got {
	t.Helper()
	ts, err := Discover(context.Background(), []string{root}, opts)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return summarize(root, ts)
}

func expect(t *testing.T, have []got, want ...got) {
	t.Helper()
	if want == nil {
		want = []got{}
	}
	if fmt.Sprint(have) != fmt.Sprint(want) {
		t.Fatalf("targets = %v, want %v", have, want)
	}
}

func repo(rel string) got    { return got{rel, TargetRepo} }
func project(rel string) got { return got{rel, TargetProject} }

func TestDiscoverRepoKinds(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	main := testutil.NewRepo(t)
	fakeRepo(t, root, "plain")
	// Real repository (a clone), a linked worktree of another repository
	// and a submodule-style .git file.
	main.Git("clone", "-q", main.Dir, filepath.Join(root, "real"))
	main.Git("worktree", "add", "-q", "-b", "wt", filepath.Join(root, "linked"))
	mkdir(t, root, "sub")
	testutil.WriteFile(t, root, "sub/.git", "gitdir: ../real/.git/modules/sub\n")
	// A fake .git file is ignored.
	testutil.WriteFile(t, root, "fake/.git", "just some text\n")
	expect(t, discoverOK(t, root, DiscoverOptions{}),
		repo("linked"), repo("plain"), repo("real"), repo("sub"))
}

func TestDiscoverProjectMarkers(t *testing.T) {
	isolateHome(t)
	markers := []string{
		"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "setup.py",
		"requirements.txt", "Pipfile", "Gemfile", "composer.json", "pom.xml",
		"build.gradle", "build.gradle.kts", "settings.gradle",
		"settings.gradle.kts", "App.csproj", "All.sln", "mix.exs",
		"pubspec.yaml", "Package.swift", "deno.json", "deno.jsonc",
		"CMakeLists.txt",
	}
	for _, m := range markers {
		t.Run(m, func(t *testing.T) {
			root := testutil.ResolvedTempDir(t)
			touch(t, root, "proj/"+m)
			expect(t, discoverOK(t, root, DiscoverOptions{}), project("proj"))
		})
	}
}

func TestDiscoverMakefile(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	touch(t, root, "alone/Makefile")
	touch(t, root, "with/Makefile")
	touch(t, root, "with/go.mod")
	touch(t, root, "readme/README.md")
	expect(t, discoverOK(t, root, DiscoverOptions{}), project("with"))
}

func TestDiscoverNesting(t *testing.T) {
	isolateHome(t)
	tests := []struct {
		name  string
		build func(t *testing.T, root string)
		opts  DiscoverOptions
		want  []got
	}{
		{"project inside repo is not a target", func(t *testing.T, r string) {
			fakeRepo(t, r, "r")
			touch(t, r, "r/pkg/package.json")
		}, DiscoverOptions{DescendIntoRepos: true}, []got{repo("r")}},
		{"repo inside repo hidden by default", func(t *testing.T, r string) {
			fakeRepo(t, r, "r")
			fakeRepo(t, r, "r/inner")
		}, DiscoverOptions{}, []got{repo("r")}},
		{"repo inside repo with descend", func(t *testing.T, r string) {
			fakeRepo(t, r, "r")
			fakeRepo(t, r, "r/inner")
			fakeRepo(t, r, "r/a/b/deep")
		}, DiscoverOptions{DescendIntoRepos: true}, []got{repo("r"), repo("r/a/b/deep"), repo("r/inner")}},
		{"nested project is a leaf", func(t *testing.T, r string) {
			touch(t, r, "mono/package.json")
			touch(t, r, "mono/packages/a/package.json")
			touch(t, r, "mono/packages/b/go.mod")
		}, DiscoverOptions{}, []got{project("mono")}},
		{"repo inside project hidden by default", func(t *testing.T, r string) {
			touch(t, r, "mono/go.mod")
			fakeRepo(t, r, "mono/sub")
		}, DiscoverOptions{}, []got{project("mono")}},
		{"repo inside project with descend", func(t *testing.T, r string) {
			touch(t, r, "mono/go.mod")
			fakeRepo(t, r, "mono/sub")
			touch(t, r, "mono/other/package.json")
		}, DiscoverOptions{DescendIntoRepos: true}, []got{project("mono"), repo("mono/sub")}},
		{"repo with marker is a repo only", func(t *testing.T, r string) {
			fakeRepo(t, r, "r")
			touch(t, r, "r/go.mod")
		}, DiscoverOptions{}, []got{repo("r")}},
		{"marker in root is ignored", func(t *testing.T, r string) {
			touch(t, r, "package.json")
			fakeRepo(t, r, "a")
		}, DiscoverOptions{}, []got{repo("a")}},
		{"marker directory is not a marker", func(t *testing.T, r string) {
			mkdir(t, r, "p/go.mod")
		}, DiscoverOptions{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := testutil.ResolvedTempDir(t)
			tt.build(t, root)
			expect(t, discoverOK(t, root, tt.opts), tt.want...)
		})
	}
}

func TestDiscoverRootIsRepo(t *testing.T) {
	isolateHome(t)
	root := fakeRepo(t, testutil.ResolvedTempDir(t), "r")
	fakeRepo(t, root, "inner")
	ts, err := Discover(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil || len(ts) != 1 || ts[0].Path != root || ts[0].Kind != TargetRepo {
		t.Fatalf("got %v, %v", ts, err)
	}
	ts, err = Discover(context.Background(), []string{root}, DiscoverOptions{DescendIntoRepos: true})
	if err != nil || len(ts) != 2 {
		t.Fatalf("descend: got %v, %v", ts, err)
	}
}

func TestDiscoverPrunedDirs(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	fakeRepo(t, root, "keep")
	for _, huge := range HugeDirNames {
		if huge == ".git" {
			continue
		}
		fakeRepo(t, root, huge+"/pkg")
	}
	fakeRepo(t, root, "node_modules/pkg/.git-holder/x")
	fakeRepo(t, root, "mine/custom/x")
	fakeRepo(t, root, "a/tmp/x")
	expect(t, discoverOK(t, root, DiscoverOptions{SkipDirs: []string{"custom", "tmp"}}), repo("keep"))
}

func TestDiscoverExclude(t *testing.T) {
	isolateHome(t)
	tests := []struct {
		name     string
		patterns []string
		want     []got
	}{
		{"double star", []string{"**/scratch"}, []got{repo("a/q/b/ok"), repo("tmp/x")}},
		{"base name", []string{"tmp"}, []got{repo("a/q/b/ok"), repo("a/scratch/x"), repo("scratch/x")}},
		{"anchored", []string{"a/*/b"}, []got{repo("a/scratch/x"), repo("scratch/x"), repo("tmp/x")}},
		{"invalid ignored", []string{"["}, []got{repo("a/q/b/ok"), repo("a/scratch/x"), repo("scratch/x"), repo("tmp/x")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := testutil.ResolvedTempDir(t)
			fakeRepo(t, root, "a/q/b/ok")
			fakeRepo(t, root, "a/scratch/x")
			fakeRepo(t, root, "scratch/x")
			fakeRepo(t, root, "tmp/x")
			expect(t, discoverOK(t, root, DiscoverOptions{Exclude: tt.patterns}), tt.want...)
		})
	}
}

func TestDiscoverExcludeNeverHidesRoot(t *testing.T) {
	isolateHome(t)
	root := fakeRepo(t, testutil.ResolvedTempDir(t), "scratch")
	ts, err := Discover(context.Background(), []string{root}, DiscoverOptions{Exclude: []string{"scratch", "**"}})
	if err != nil || len(ts) != 1 {
		t.Fatalf("got %v, %v", ts, err)
	}
}

// deepPath builds "d1/d2/.../dn".
func deepPath(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("d%d", i+1)
	}
	return strings.Join(parts, "/")
}

func TestDiscoverMaxDepth(t *testing.T) {
	isolateHome(t)
	tests := []struct {
		name  string
		depth int
		build func(t *testing.T, root, rel string)
		max   int
		want  bool
	}{
		{"repo at default limit", 6, func(t *testing.T, r, p string) { fakeRepo(t, r, p) }, 0, true},
		{"repo beyond default limit", 7, func(t *testing.T, r, p string) { fakeRepo(t, r, p) }, 0, false},
		{"project at default limit", 6, func(t *testing.T, r, p string) { touch(t, r, p+"/go.mod") }, 0, true},
		{"project beyond default limit", 7, func(t *testing.T, r, p string) { touch(t, r, p+"/go.mod") }, 0, false},
		{"repo at custom limit", 2, func(t *testing.T, r, p string) { fakeRepo(t, r, p) }, 2, true},
		{"repo beyond custom limit", 3, func(t *testing.T, r, p string) { fakeRepo(t, r, p) }, 2, false},
		{"project at depth 1", 1, func(t *testing.T, r, p string) { touch(t, r, p+"/go.mod") }, 1, true},
		{"project beyond depth 1", 2, func(t *testing.T, r, p string) { touch(t, r, p+"/go.mod") }, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := testutil.ResolvedTempDir(t)
			rel := deepPath(tt.depth)
			tt.build(t, root, rel)
			have := discoverOK(t, root, DiscoverOptions{MaxDepth: tt.max})
			if (len(have) == 1) != tt.want || len(have) > 1 {
				t.Fatalf("targets = %v, want found=%v", have, tt.want)
			}
		})
	}
}

func TestDiscoverSymlinkNotFollowed(t *testing.T) {
	isolateHome(t)
	outside := testutil.ResolvedTempDir(t)
	fakeRepo(t, outside, "elsewhere")
	root := testutil.ResolvedTempDir(t)
	fakeRepo(t, root, "real")
	if err := os.Symlink(filepath.Join(outside, "elsewhere"), filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	expect(t, discoverOK(t, root, DiscoverOptions{}), repo("real"))
}

func TestDiscoverSymlinkedRootIsResolved(t *testing.T) {
	isolateHome(t)
	real := testutil.ResolvedTempDir(t)
	fakeRepo(t, real, "r")
	link := filepath.Join(testutil.ResolvedTempDir(t), "ws")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	ts, err := Discover(context.Background(), []string{link}, DiscoverOptions{})
	if err != nil || len(ts) != 1 {
		t.Fatalf("got %v, %v", ts, err)
	}
	want := findings.Scope{Type: findings.ScopeRoot, Path: real}
	if ts[0].Scope != want || ts[0].Path != filepath.Join(real, "r") {
		t.Fatalf("target = %+v, want scope %+v", ts[0], want)
	}
}

func TestDiscoverDuplicateAndNestedRoots(t *testing.T) {
	isolateHome(t)
	a := testutil.ResolvedTempDir(t)
	fakeRepo(t, a, "one")
	fakeRepo(t, a, "inner/two")
	inner := filepath.Join(a, "inner")

	t.Run("duplicate", func(t *testing.T) {
		ts, err := Discover(context.Background(), []string{a, a, a + string(filepath.Separator)}, DiscoverOptions{})
		if err != nil || len(ts) != 2 {
			t.Fatalf("got %v, %v", ts, err)
		}
	})
	t.Run("symlink spelling", func(t *testing.T) {
		link := filepath.Join(testutil.ResolvedTempDir(t), "l")
		if err := os.Symlink(a, link); err != nil {
			t.Skipf("cannot create symlinks here: %v", err)
		}
		ts, err := Discover(context.Background(), []string{a, link}, DiscoverOptions{})
		if err != nil || len(ts) != 2 {
			t.Fatalf("got %v, %v", ts, err)
		}
	})
	t.Run("nested innermost scope", func(t *testing.T) {
		for _, roots := range [][]string{{a, inner}, {inner, a}} {
			ts, err := Discover(context.Background(), roots, DiscoverOptions{})
			if err != nil || len(ts) != 2 {
				t.Fatalf("got %v, %v", ts, err)
			}
			for _, tg := range ts {
				want := a
				if strings.HasSuffix(tg.Path, "two") {
					want = inner
				}
				if tg.Scope.Path != want {
					t.Errorf("%s scope = %s, want %s", tg.Path, tg.Scope.Path, want)
				}
			}
		}
	})
}

func TestDiscoverMissingRootPartialResult(t *testing.T) {
	isolateHome(t)
	good := testutil.ResolvedTempDir(t)
	fakeRepo(t, good, "r")
	missing := filepath.Join(good, "does-not-exist")
	file := filepath.Join(good, "file.txt")
	touch(t, good, "file.txt")
	ts, err := Discover(context.Background(), []string{missing, good, file}, DiscoverOptions{})
	if len(ts) != 1 {
		t.Fatalf("targets = %v, want the valid root's repo", ts)
	}
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), file) {
		t.Fatalf("error must name both bad roots, got %v", err)
	}
}

func TestDiscoverSortedAndDeterministic(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	for i := 0; i < 40; i++ {
		fakeRepo(t, root, fmt.Sprintf("g%d/r%02d", i%5, 39-i))
	}
	touch(t, root, "zz/go.mod")
	first, err := Discover(context.Background(), []string{root}, DiscoverOptions{Concurrency: 8})
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, len(first))
	for i, tg := range first {
		paths[i] = tg.Path
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("not sorted: %v", paths)
	}
	for i := 0; i < 10; i++ {
		again, err := Discover(context.Background(), []string{root}, DiscoverOptions{Concurrency: 1 + i})
		if err != nil || fmt.Sprint(again) != fmt.Sprint(first) {
			t.Fatalf("run %d differs: %v", i, err)
		}
	}
}

// TestDiscoverNestingIsSchedulingIndependent repeats a walk with many
// workers over projects containing marker-heavy subtrees; the leaf rule must
// hold whichever directories the walkers enter first (run with -race).
func TestDiscoverNestingIsSchedulingIndependent(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	for i := 0; i < 20; i++ {
		p := fmt.Sprintf("proj%02d", i)
		touch(t, root, p+"/go.mod")
		for j := 0; j < 5; j++ {
			touch(t, root, fmt.Sprintf("%s/sub%d/package.json", p, j))
			fakeRepo(t, root, fmt.Sprintf("%s/sub%d/r", p, j))
		}
	}
	for i := 0; i < 20; i++ {
		have := discoverOK(t, root, DiscoverOptions{Concurrency: 16})
		if len(have) != 20 {
			t.Fatalf("run %d: %d targets, want 20", i, len(have))
		}
		for _, g := range have {
			if g.kind != TargetProject || strings.Contains(g.rel, "/") {
				t.Fatalf("run %d: unexpected target %v", i, g)
			}
		}
	}
}

func TestDiscoverHomeLibrary(t *testing.T) {
	home := isolateHome(t)
	fakeRepo(t, home, "Library/skipped")
	fakeRepo(t, home, "AppData/skipped")
	fakeRepo(t, home, "dev/Library/kept")
	fakeRepo(t, home, "dev/proj")
	expect(t, discoverOK(t, home, DiscoverOptions{}), repo("dev/Library/kept"), repo("dev/proj"))
}

func TestDiscoverUnreadableDir(t *testing.T) {
	isolateHome(t)
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not restrict directories on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := testutil.ResolvedTempDir(t)
	fakeRepo(t, root, "ok")
	locked := mkdir(t, root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	var mu sync.Mutex
	var errPaths []string
	opts := DiscoverOptions{OnError: func(p string, err error) {
		mu.Lock()
		defer mu.Unlock()
		errPaths = append(errPaths, p)
	}}
	expect(t, discoverOK(t, root, opts), repo("ok"))
	if len(errPaths) == 0 || errPaths[0] != locked {
		t.Fatalf("OnError paths = %v, want %s", errPaths, locked)
	}
}

func TestDiscoverCancelled(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	fakeRepo(t, root, "r")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ts, err := Discover(ctx, []string{root}, DiscoverOptions{})
	if !errors.Is(err, context.Canceled) || ts != nil {
		t.Fatalf("got %v, %v; want nil, context.Canceled", ts, err)
	}
}

func TestDiscoverScopeIsResolvedRoot(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	touch(t, root, "p/go.mod")
	ts, err := Discover(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil || len(ts) != 1 {
		t.Fatalf("got %v, %v", ts, err)
	}
	want := findings.Scope{Type: findings.ScopeRoot, Path: root}
	if ts[0].Scope != want || ts[0].Kind != TargetProject || ts[0].Path != filepath.Join(root, "p") {
		t.Fatalf("target = %+v", ts[0])
	}
}
