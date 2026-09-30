package walk

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// smallCacheLimit lowers maxCacheBytes for the test.
func smallCacheLimit(t *testing.T, n int64) {
	t.Helper()
	orig := maxCacheBytes
	maxCacheBytes = n
	t.Cleanup(func() { maxCacheBytes = orig })
}

func TestStoreCacheSkipsAndRemovesOversizedFile(t *testing.T) {
	root, opts := cachedTree(t)
	mustSize(t, root, opts)
	file := cacheFilePath(opts.CacheDir, root)
	if _, err := os.Stat(file); err != nil {
		t.Fatal("a normal cache should have been written")
	}
	smallCacheLimit(t, 64)
	if err := storeCache(file, root, map[string]*dirRecord{"": {Subdirs: []string{"a", "b", "c"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("oversized cache must not be written and the old file must go, stat err = %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(opts.CacheDir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

// TestEncodedSizeLowerBoundNeverExceedsTheDocument pins that the estimate is a
// true lower bound, including for nil records and records with subdirectories,
// because an overestimate would drop a cache that fits.
func TestEncodedSizeLowerBoundNeverExceedsTheDocument(t *testing.T) {
	tests := map[string]map[string]*dirRecord{
		"empty":         {},
		"no subdirs":    {"": {}},
		"nil record":    {"a": nil},
		"one subdir":    {"": {Subdirs: []string{"x"}}},
		"many subdirs":  {"": {Subdirs: []string{"a", "bb", "ccc"}}, "a": {Subdirs: []string{"d"}}, "a/d": {}},
		"mixed":         {"": {Subdirs: []string{"a"}}, "a": nil, "b": {}},
		"escaped names": {"": {Subdirs: []string{"q\"uote"}}},
	}
	for name, dirs := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(cacheFile{Version: cacheVersion, Root: "/r", Dirs: dirs})
			if err != nil {
				t.Fatal(err)
			}
			if got := encodedSizeLowerBound(dirs); got > int64(len(data)) {
				t.Errorf("lower bound %d exceeds the encoded size %d: %s", got, len(data), data)
			}
		})
	}
}

// TestDocumentJustUnderTheLimitIsCached pins the boundary: a document that
// fits exactly is written, one byte less room drops it, so the cheap estimate
// never rejects a document that would have fit.
func TestDocumentJustUnderTheLimitIsCached(t *testing.T) {
	dirs := map[string]*dirRecord{
		"":  {Subdirs: []string{"a", "b"}},
		"a": {Subdirs: []string{"c"}},
		"b": {},
	}
	const root = "/r"
	data, err := json.Marshal(cacheFile{Version: cacheVersion, Root: root, Dirs: dirs})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "cache.json")

	smallCacheLimit(t, int64(len(data)))
	if err := storeCache(file, root, dirs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("a document of exactly the limit must be cached: %v", err)
	}

	smallCacheLimit(t, int64(len(data))-1)
	if err := storeCache(file, root, dirs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("a document over the limit must be dropped, stat err = %v", err)
	}
}

func TestDirSizeWithOversizedCacheWritesNothing(t *testing.T) {
	root, opts := cachedTree(t)
	smallCacheLimit(t, 64)
	want := uncached(t, root)
	for range 2 {
		if got := mustSize(t, root, opts); got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
	entries, _ := os.ReadDir(opts.CacheDir)
	if len(entries) != 0 {
		t.Errorf("cache dir holds %d entries, want none", len(entries))
	}
}

// TestOversizedTreeIsNeverMarshalled pins that a tree known to exceed the cache
// limit does not pay for encoding its document on every scan.
func TestOversizedTreeIsNeverMarshalled(t *testing.T) {
	root, opts := cachedTree(t)
	smallCacheLimit(t, 64)
	var calls int
	orig := marshalCache
	marshalCache = func(cf cacheFile) ([]byte, error) { calls++; return orig(cf) }
	t.Cleanup(func() { marshalCache = orig })
	for range 3 {
		mustSize(t, root, opts)
	}
	if calls != 0 {
		t.Errorf("marshalled %d times although the document cannot fit", calls)
	}
}

func TestUnchangedScanDoesNotRewriteCacheButTouchesIt(t *testing.T) {
	root, opts := cachedTree(t)
	mustSize(t, root, opts)
	file := cacheFilePath(opts.CacheDir, root)
	old := time.Now().Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	mustSize(t, root, opts)
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("an unchanged scan rewrote the cache file (new inode)")
	}
	if !after.ModTime().After(old.Add(time.Hour)) {
		t.Errorf("cache mtime %v was not refreshed, pruning would treat it as unused", after.ModTime())
	}
}

func TestChangedOrRemovedDirectoryRewritesCache(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, root string)
	}{
		{"new file in a directory", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "a", "new.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			ageTree(t, root)
		}},
		{"removed directory", func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(root, "d")); err != nil {
				t.Fatal(err)
			}
			ageTree(t, root)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, opts := cachedTree(t)
			mustSize(t, root, opts)
			file := cacheFilePath(opts.CacheDir, root)
			// Compare contents, not file identity: Windows may hand the
			// replaced file the same file index, so SameFile is unreliable.
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			tt.change(t, root)
			mustSize(t, root, opts)
			after, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before, after) {
				t.Error("changed tree did not rewrite the cache")
			}
			if got := mustSize(t, root, opts); got != uncached(t, root) {
				t.Errorf("cache stale after rewrite: %+v", got)
			}
		})
	}
}

// writeCache creates a cache file for root with the given age.
func writeCache(t *testing.T, dir, root string, age time.Duration) string {
	t.Helper()
	file := cacheFilePath(dir, root)
	if err := storeCache(file, root, map[string]*dirRecord{"": {Subdirs: []string{}}}); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(file, mt, mt); err != nil {
		t.Fatal(err)
	}
	return file
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// writeAged creates a plain file with the given age.
func writeAged(t *testing.T, path, content string, age time.Duration) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
	return path
}

// pruneDir holds a cache dir with one file per pruning case.
type pruneDir struct {
	dir                      string
	fresh, unrelated         string
	old, vanished, junk, tmp string
}

func newPruneDir(t *testing.T) pruneDir {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache")
	live := t.TempDir()
	d := pruneDir{dir: dir}
	d.fresh = writeCache(t, dir, live, time.Hour)
	d.old = writeCache(t, dir, live+"-old", 40*24*time.Hour)
	d.vanished = writeCache(t, dir, filepath.Join(t.TempDir(), "gone"), time.Hour)
	d.unrelated = writeAged(t, filepath.Join(dir, "notes.txt"), "mine", 90*24*time.Hour)
	d.junk = writeAged(t, filepath.Join(dir, "dirsize-v1-deadbeefdeadbeef.json"), "not json", time.Hour)
	d.tmp = writeAged(t, filepath.Join(dir, "dirsize-123.tmp"), "half", 3*time.Hour)
	return d
}

func TestPruneCacheAgesOutVerdictFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	vdir := filepath.Join(dir, "verdicts")
	if err := os.MkdirAll(vdir, 0o700); err != nil {
		t.Fatal(err)
	}
	fresh := writeAged(t, filepath.Join(vdir, "a.json"), "{}", time.Hour)
	old := writeAged(t, filepath.Join(vdir, "b.json"), "{}", 40*24*time.Hour)
	oldTmp := writeAged(t, filepath.Join(vdir, "tmp-1"), "x", 3*time.Hour)
	freshTmp := writeAged(t, filepath.Join(vdir, "tmp-2"), "x", time.Minute)
	other := writeAged(t, filepath.Join(vdir, "notes.txt"), "mine", 90*24*time.Hour)

	if _, err := PruneCache(dir, PruneOptions{CheckRoots: true}); err != nil {
		t.Fatal(err)
	}
	if exists(old) || exists(oldTmp) {
		t.Fatal("old verdict and stale tmp file must be pruned")
	}
	if !exists(fresh) || !exists(freshTmp) || !exists(other) {
		t.Fatal("fresh verdicts, running tmp files and foreign files must stay")
	}
}

func TestListStaleCacheWithoutRootChecksUsesAgeOnly(t *testing.T) {
	d := newPruneDir(t)
	stale, err := ListStaleCache(d.dir, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 2 {
		t.Fatalf("age-only listing = %+v, want the old file and the temp file", stale)
	}
	if !exists(d.old) || !exists(d.tmp) {
		t.Fatal("listing must not delete anything")
	}
}

func TestPruneCacheRemovesOldVanishedAndBrokenFiles(t *testing.T) {
	d := newPruneDir(t)
	removed, err := PruneCache(d.dir, PruneOptions{CheckRoots: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 4 {
		t.Errorf("removed %d files, want 4: %+v", len(removed), removed)
	}
	for _, p := range []string{d.old, d.vanished, d.junk, d.tmp} {
		if exists(p) {
			t.Errorf("%s should be pruned", filepath.Base(p))
		}
	}
	for _, p := range []string{d.fresh, d.unrelated} {
		if !exists(p) {
			t.Errorf("%s must survive", filepath.Base(p))
		}
	}
}

func TestPruneCacheReasons(t *testing.T) {
	d := newPruneDir(t)
	removed, _ := PruneCache(d.dir, PruneOptions{CheckRoots: true})
	reasons := map[string]string{}
	for _, r := range removed {
		reasons[filepath.Base(r.Path)] = r.Reason
	}
	if got := reasons[filepath.Base(d.vanished)]; got != "root no longer exists" {
		t.Errorf("vanished reason = %q", got)
	}
	if got := reasons[filepath.Base(d.tmp)]; got != "interrupted write" {
		t.Errorf("tmp reason = %q", got)
	}
	if got := reasons[filepath.Base(d.old)]; !strings.HasPrefix(got, "unused for 40 days") {
		t.Errorf("old reason = %q", got)
	}
}

func TestPruneCacheKeepsRecentTempFilesAndMissingDir(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "dirsize-1.tmp")
	if err := os.WriteFile(tmp, []byte("in flight"), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := PruneCache(dir, PruneOptions{CheckRoots: true}); err != nil || len(removed) != 0 || !exists(tmp) {
		t.Errorf("a temp file of a running write was pruned: %v %v", removed, err)
	}
	if removed, err := PruneCache(filepath.Join(dir, "missing"), PruneOptions{}); err != nil || len(removed) != 0 {
		t.Errorf("missing dir: %v %v", removed, err)
	}
}

func TestPruneCacheNeverFollowsSymlinks(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "precious.json")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "dirsize-v1-0123456789abcdef.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	_ = os.Chtimes(target, old, old)
	if removed, _ := PruneCache(dir, PruneOptions{CheckRoots: true}); len(removed) != 0 || !exists(target) || !exists(link) {
		t.Errorf("symlink handled: removed=%v", removed)
	}
}

func TestDirSizeWriteOnlyPrunesAgeStaleFilesOncePerDir(t *testing.T) {
	root, opts := cachedTree(t)
	stale := writeCache(t, opts.CacheDir, "/somewhere/abandoned", 60*24*time.Hour)
	mustSize(t, root, opts)
	if exists(stale) {
		t.Error("first cache write should prune an abandoned cache file")
	}
	if !exists(cacheFilePath(opts.CacheDir, root)) {
		t.Error("the live cache must be written")
	}
}

// TestListStaleCacheCoversGitbloatBlobCaches: the detector's per-repository
// caches share the cache dir, so purge must list them by age, size and as
// orphaned temp files, while leaving used, recent and foreign files alone.
func TestListStaleCacheCoversGitbloatBlobCaches(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := writeAged(t, filepath.Join(dir, "gitbloat-blobs-aaaaaaaaaaaaaaaa.json"), "{}", 40*24*time.Hour)
	recent := writeAged(t, filepath.Join(dir, "gitbloat-blobs-bbbbbbbbbbbbbbbb.json"), "{}", time.Hour)
	huge := writeAged(t, filepath.Join(dir, "gitbloat-blobs-cccccccccccccccc.json"),
		strings.Repeat("x", BlobCacheMaxBytes+1), time.Hour)
	oldTmp := writeAged(t, filepath.Join(dir, "gitbloat-blobs-123.tmp"), "half", 3*time.Hour)
	newTmp := writeAged(t, filepath.Join(dir, "gitbloat-blobs-456.tmp"), "half", time.Minute)
	other := writeAged(t, filepath.Join(dir, "gitbloat-notes.json"), "mine", 90*24*time.Hour)

	for _, checkRoots := range []bool{false, true} {
		stale, err := ListStaleCache(dir, PruneOptions{CheckRoots: checkRoots})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, f := range stale {
			got[f.Path] = f.Reason
		}
		want := map[string]string{
			old:    "unused for 40 days",
			huge:   "unreadable or oversized",
			oldTmp: "interrupted write",
		}
		if len(got) != len(want) {
			t.Fatalf("CheckRoots=%v: stale = %v, want %v", checkRoots, got, want)
		}
		for p, reason := range want {
			if got[p] != reason {
				t.Errorf("CheckRoots=%v: %s reason = %q, want %q", checkRoots, filepath.Base(p), got[p], reason)
			}
		}
		for _, keep := range []string{recent, newTmp, other} {
			if _, bad := got[keep]; bad {
				t.Errorf("%s must not be stale", filepath.Base(keep))
			}
		}
	}
}
