package walk

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cachedTree returns an aged sample tree and options with a private cache.
func cachedTree(t *testing.T) (string, Options) {
	t.Helper()
	root := sampleTree(t)
	ageTree(t, root)
	return root, Options{CacheDir: filepath.Join(t.TempDir(), "cache")}
}

// uncached computes the ground truth without any cache.
func uncached(t *testing.T, root string) DirSummary { return mustSize(t, root, Options{}) }

func TestCacheColdEqualsWarmAndWarmReadsNothing(t *testing.T) {
	root, opts := cachedTree(t)
	cold := mustSize(t, root, opts)
	reads := countReads(t)
	warm := mustSize(t, root, opts)
	if cold != warm {
		t.Errorf("cold %+v != warm %+v", cold, warm)
	}
	if n := reads.Load(); n != 0 {
		t.Errorf("warm run read %d directories, want 0", n)
	}
	if cold != uncached(t, root) {
		t.Errorf("cached result differs from uncached")
	}
}

func TestCacheFileNameAndPermissions(t *testing.T) {
	root, opts := cachedTree(t)
	mustSize(t, root, opts)
	entries, err := os.ReadDir(opts.CacheDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache dir: %v %v", entries, err)
	}
	if !regexp.MustCompile(`^dirsize-v1-[0-9a-f]{16}\.json$`).MatchString(entries[0].Name()) {
		t.Errorf("unexpected cache file name %q", entries[0].Name())
	}
	if runtime.GOOS == "windows" {
		return
	}
	di, _ := os.Stat(opts.CacheDir)
	fi, _ := os.Stat(filepath.Join(opts.CacheDir, entries[0].Name()))
	if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
		t.Errorf("modes: dir %v file %v, want 0700 / 0600", di.Mode().Perm(), fi.Mode().Perm())
	}
}

func TestCacheInvalidation(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(t *testing.T, root string)
		wantReads int64
	}{
		{"file added", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "a", "new"), 7000) }, 1},
		{"subdirectory deleted", func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(root, "a", "b")); err != nil {
				t.Fatal(err)
			}
		}, 1},
		{"deep subdirectory touched", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "a", "b", "c", "extra"), 123)
		}, 1},
		{"file removed", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "d", "f4")); err != nil {
				t.Fatal(err)
			}
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, opts := cachedTree(t)
			mustSize(t, root, opts)
			tt.mutate(t, root)
			reads := countReads(t)
			got := mustSize(t, root, opts)
			n := reads.Load() // before the uncached reference read below
			if want := uncached(t, root); got.SizeBytes != want.SizeBytes || got.Files != want.Files {
				t.Errorf("got %+v, want %+v", got, want)
			}
			if n != tt.wantReads {
				t.Errorf("re-read %d directories, want only the changed one (%d)", n, tt.wantReads)
			}
		})
	}
}

func TestCacheDropsRecordsOfRemovedDirectories(t *testing.T) {
	root, opts := cachedTree(t)
	mustSize(t, root, opts)
	if err := os.RemoveAll(filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	mustSize(t, root, opts)
	dirs := loadCache(cacheFilePath(opts.CacheDir, root), root)
	for rel := range dirs {
		if rel == "a" || strings.HasPrefix(rel, "a/") {
			t.Errorf("record %q of a removed directory survived", rel)
		}
	}
	if len(dirs) == 0 {
		t.Error("cache should still hold the remaining directories")
	}
}

func TestCacheRacyRecordsAreAlwaysReread(t *testing.T) {
	root := sampleTree(t) // fresh mtimes: every directory is racy
	opts := Options{CacheDir: t.TempDir()}
	mustSize(t, root, opts)
	dirs := loadCache(cacheFilePath(opts.CacheDir, root), root)
	if len(dirs) != 6 { // root, a, a/b, a/b/c, d, e
		t.Fatalf("cached %d directories", len(dirs))
	}
	for rel, rec := range dirs {
		if !rec.Racy {
			t.Errorf("record %q should be racy", rel)
		}
	}
	reads := countReads(t)
	mustSize(t, root, opts)
	if n := reads.Load(); n != 6 {
		t.Errorf("racy warm run read %d directories, want all 6", n)
	}
}

func TestCacheRacyFlagFollowsScanStart(t *testing.T) {
	root, opts := cachedTree(t)
	// Pretend the scan happens right after the (aged) directories were
	// modified: they are then within the racy window.
	orig := now
	t.Cleanup(func() { now = orig })
	now = func() time.Time { return time.Now().Add(-time.Hour) }
	mustSize(t, root, opts)
	for rel, rec := range loadCache(cacheFilePath(opts.CacheDir, root), root) {
		if !rec.Racy {
			t.Errorf("record %q should be racy relative to the scan start", rel)
		}
	}
}

func TestCacheCorruptFilesAreIgnoredAndRebuilt(t *testing.T) {
	valid := func(root string, opts Options) []byte {
		mustSize(t, root, opts)
		b, err := os.ReadFile(cacheFilePath(opts.CacheDir, root))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	tests := []struct {
		name string
		make func(good []byte) []byte
	}{
		{"garbage", func([]byte) []byte { return []byte("not json at all") }},
		{"truncated", func(g []byte) []byte { return g[:len(g)/2] }},
		{"empty", func([]byte) []byte { return nil }},
		{"unknown version", func([]byte) []byte { return []byte(`{"version":99,"root":"x","dirs":{}}`) }},
		{"wrong types", func([]byte) []byte { return []byte(`{"version":1,"root":5,"dirs":"nope"}`) }},
		{"wrong record types", func([]byte) []byte {
			return []byte(`{"version":1,"dirs":{"":{"mtime_unix_nano":"x","direct_bytes":[]}}}`)
		}},
		{"null record", func([]byte) []byte { return []byte(`{"version":1,"dirs":{"":null}}`) }},
		{"path traversal in subdirs", func([]byte) []byte {
			return []byte(`{"version":1,"root":"ROOT","dirs":{"":{"subdirs":["../.."]}}}`)
		}},
		{"oversized", func(g []byte) []byte { return append(g, make([]byte, maxCacheBytes)...) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, opts := cachedTree(t)
			want := uncached(t, root)
			good := valid(root, opts)
			file := cacheFilePath(opts.CacheDir, root)
			content := strings.ReplaceAll(string(tt.make(good)), "ROOT", strings.ReplaceAll(root, `\`, `\\`))
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := mustSize(t, root, opts); got != want {
				t.Errorf("got %+v, want %+v", got, want)
			}
			if loadCache(file, root) == nil {
				t.Error("cache was not rebuilt")
			}
		})
	}
}

func TestCacheForOtherRootIsIgnored(t *testing.T) {
	root, opts := cachedTree(t)
	mustSize(t, root, opts)
	file := cacheFilePath(opts.CacheDir, root)
	if loadCache(file, root+"-other") != nil {
		t.Error("cache for a different root must not load")
	}
}

// The documented limitation: an in-place append does not change the
// directory mtime, so a non-Fresh call may keep reporting the old newest
// mtime, while Fresh sees the new one and refreshes the cache.
func TestFreshSeesInPlaceAppends(t *testing.T) {
	root, opts := cachedTree(t)
	warmup := mustSize(t, root, opts)

	logFile := filepath.Join(root, "a", "b", "f2")
	dirBefore, _ := os.Stat(filepath.Dir(logFile))
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("more log"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	dirAfter, _ := os.Stat(filepath.Dir(logFile))
	if !dirAfter.ModTime().Equal(dirBefore.ModTime()) {
		t.Skip("filesystem bumps the directory mtime on in-place writes")
	}
	appended := time.Now().Add(-time.Minute)

	// Documented lower bound: not asserted strictly, but it must never be
	// newer than what really happened.
	stale := mustSize(t, root, opts)
	if stale.NewestModTime.After(time.Now()) {
		t.Errorf("stale newest %v is in the future", stale.NewestModTime)
	}

	fresh := mustSize(t, root, Options{CacheDir: opts.CacheDir, Fresh: true})
	if !fresh.NewestModTime.After(appended) || !fresh.NewestModTime.After(warmup.NewestModTime) {
		t.Errorf("fresh newest %v should reflect the append (warmup %v)", fresh.NewestModTime, warmup.NewestModTime)
	}

}

// TestFreshLeavesCacheAlone: a Fresh call neither reads nor writes the cache.
// Every production caller is Fresh, so writing here rewrote megabytes of
// records on each scan that no later scan could use.
func TestFreshLeavesCacheAlone(t *testing.T) {
	root, opts := cachedTree(t)
	fresh := Options{CacheDir: opts.CacheDir, Fresh: true}

	mustSize(t, root, fresh)
	if entries, _ := os.ReadDir(opts.CacheDir); len(entries) != 0 {
		t.Errorf("Fresh wrote cache files: %v", entries)
	}

	mustSize(t, root, opts)
	file := cacheFilePath(opts.CacheDir, root)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	writeFile(t, filepath.Join(root, "a", "new"), 7000)
	mustSize(t, root, fresh)
	after, _ := os.ReadFile(file)
	fi, _ := os.Stat(file)
	if string(before) != string(after) || !fi.ModTime().Equal(old) {
		t.Errorf("Fresh modified an existing cache file (mtime %v)", fi.ModTime())
	}
}

func TestFreshReReadsEverything(t *testing.T) {
	root, opts := cachedTree(t)
	mustSize(t, root, opts)
	reads := countReads(t)
	mustSize(t, root, Options{CacheDir: opts.CacheDir, Fresh: true})
	if n := reads.Load(); n != 6 {
		t.Errorf("Fresh read %d directories, want 6", n)
	}
}

func TestCacheWriteFailureDoesNotFailDirSize(t *testing.T) {
	root := sampleTree(t)
	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, blocker, 1)
	// A cache dir below a regular file can never be created.
	got, err := DirSize(context.Background(), root, Options{CacheDir: filepath.Join(blocker, "cache")})
	if err != nil || got.Files != 5 {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestCacheConcurrentDirSizeSamePath(t *testing.T) {
	root, opts := cachedTree(t)
	want := uncached(t, root)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := DirSize(context.Background(), root, Options{CacheDir: opts.CacheDir, Fresh: i%2 == 0})
			if err != nil || got != want {
				t.Errorf("got %+v err %v, want %+v", got, err, want)
			}
		}()
	}
	wg.Wait()
	if loadCache(cacheFilePath(opts.CacheDir, root), root) == nil {
		t.Error("cache unreadable after concurrent writers")
	}
	leftovers, _ := filepath.Glob(filepath.Join(opts.CacheDir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestCacheNotPersistedAboveDirectoryCap(t *testing.T) {
	dirs := make(map[string]*dirRecord, maxCacheDirs+1)
	for i := 0; i <= maxCacheDirs; i++ {
		dirs[strconv.Itoa(i)] = &dirRecord{}
	}
	file := filepath.Join(t.TempDir(), "c.json")
	if err := storeCache(file, "/r", dirs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("cache above the directory cap was persisted")
	}
}
