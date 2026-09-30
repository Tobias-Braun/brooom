package walk

import (
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
			before, _ := os.Stat(file)
			tt.change(t, root)
			mustSize(t, root, opts)
			after, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(before, after) {
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
