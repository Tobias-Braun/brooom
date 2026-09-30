package scope

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// fuzzComponents are the building blocks the fuzzer strings together: link
// names (in, out, chain, loop, dangling), directory names, dot components, an
// empty component and a name beyond NAME_MAX.
var fuzzComponents = []string{
	"in", "out", "chain", "loop-a", "loop-b", "dangling-in", "dangling-out",
	"dir", "sub", "file", "missing", "..", ".", "", strings.Repeat("n", 300),
}

// buildFuzzTree creates the fixed tree the fuzz test runs on and returns the
// allowed root and a directory outside of it.
func buildFuzzTree(t testing.TB) (allowed, outside string) {
	t.Helper()
	requireSymlink(t)
	root := testutil.ResolvedTempDir(t)
	allowed = mkdir(t, root, "allowed")
	outside = mkdir(t, root, "outside")
	mkdir(t, allowed, "dir", "sub")
	touch(t, allowed, "dir", "file")
	touch(t, outside, "file")
	mkdir(t, outside, "dir", "sub")
	symlink(t, outside, filepath.Join(allowed, "out"))
	symlink(t, "dir", filepath.Join(allowed, "in"))
	symlink(t, "out", filepath.Join(allowed, "chain"))
	symlink(t, "loop-b", filepath.Join(allowed, "loop-a"))
	symlink(t, "loop-a", filepath.Join(allowed, "loop-b"))
	symlink(t, filepath.Join(allowed, "dir", "missing"), filepath.Join(allowed, "dangling-in"))
	symlink(t, filepath.Join(outside, "missing"), filepath.Join(allowed, "dangling-out"))
	symlink(t, filepath.Join(allowed, "dir"), filepath.Join(outside, "in"))
	symlink(t, allowed, filepath.Join(outside, "allowed-link"))
	return allowed, outside
}

// realPath resolves the longest existing prefix of p with filepath.EvalSymlinks
// and appends the rest, as an oracle independent of the guard's own resolver.
func realPath(t testing.TB, p string) string {
	t.Helper()
	rest := ""
	for cur := p; ; cur = filepath.Dir(cur) {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(cur) == cur {
			t.Fatalf("no existing prefix of %q", p)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
	}
}

// FuzzGuardResolve checks the one invariant that matters: whenever Resolve
// returns no error, the result lies inside an allowed location, judged by a
// filepath.Rel check on the EvalSymlinks-resolved path.
func FuzzGuardResolve(f *testing.F) {
	allowed, outside := buildFuzzTree(f)
	g, err := NewGuard(allowed)
	if err != nil {
		f.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(allowed)
	if err != nil {
		f.Fatal(err)
	}
	// Seeds: byte 0 picks the start (allowed, outside, allowed/dir), the
	// others index fuzzComponents.
	for _, seed := range [][]byte{
		{0, 0}, {0, 1, 7}, {0, 2}, {0, 3, 9}, {0, 4}, {0, 5}, {0, 6, 10},
		{0, 11, 11, 11}, {0, 0, 10, 10, 10, 10}, {1, 0}, {1, 10, 1}, {2, 11, 1},
		{0, 1, 10, 10}, {0, 3, 10}, {0, 14}, {0, 12, 12, 7}, {2, 10, 10, 10, 1},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		starts := []string{allowed, outside, filepath.Join(allowed, "dir")}
		path := starts[int(data[0])%len(starts)]
		for _, b := range data[1:] {
			path += "/" + fuzzComponents[int(b)%len(fuzzComponents)]
		}
		got, err := g.Resolve(path)
		if err != nil {
			return
		}
		rel, err := filepath.Rel(real, realPath(t, got))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("Resolve(%q) = %q was allowed but its real path is outside %q (rel %q, %v)", path, got, real, rel, err)
		}
	})
}
