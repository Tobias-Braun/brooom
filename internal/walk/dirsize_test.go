package walk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestDirSizeHasVCS proves that the size pass reports a .git entry (directory
// or file, at any depth) and that the answer survives the cache.
func TestDirSizeHasVCS(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  bool
	}{
		{"none", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "a", "f"), 1) }, false},
		{"git dir at root", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, ".git", "HEAD"), 1) }, true},
		{"git file deep", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "a", "b", ".git"), 1) }, true},
		{"similar name", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, ".github", "x"), 1) }, false},
		{"hg dir deep", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "a", ".hg", "store"), 1) }, true},
		{"jj dir", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "a", ".jj", "repo"), 1) }, true},
		{"svn dir", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, ".svn", "wc.db"), 1) }, true},
		{"bare repo deep", func(t *testing.T, root string) { makeBare(t, filepath.Join(root, "a", "clone.git")) }, true},
		{"bare repo is the root", func(t *testing.T, root string) { makeBare(t, root) }, true},
		{"HEAD and objects only", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "HEAD"), 1)
			writeFile(t, filepath.Join(root, "objects", "x"), 1)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, cache := t.TempDir(), t.TempDir()
			tc.setup(t, root)
			ageTree(t, root)
			for _, opts := range []Options{{}, {CacheDir: cache}, {CacheDir: cache}, {CacheDir: cache, Fresh: true}} {
				sum := mustSize(t, root, opts)
				if sum.HasVCS != tc.want {
					t.Fatalf("HasVCS = %v, want %v (opts %+v)", sum.HasVCS, tc.want, opts)
				}
			}
		})
	}
}

// makeBare lays out the shape of a bare git repository (HEAD, objects/, refs/).
func makeBare(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "HEAD"), 1)
	writeFile(t, filepath.Join(dir, "objects", "pack", "p"), 1)
	writeFile(t, filepath.Join(dir, "refs", "heads", "main"), 1)
}

func mustSize(t *testing.T, path string, opts Options) DirSummary {
	t.Helper()
	s, err := DirSize(context.Background(), path, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDirSizeMatchesSerialReference(t *testing.T) {
	root := sampleTree(t)
	wantSize, wantFiles := referenceSize(t, root)
	for _, workers := range []int{1, 8} {
		got := mustSize(t, root, Options{Concurrency: workers})
		if got.SizeBytes != wantSize || got.Files != wantFiles {
			t.Errorf("workers=%d: got %d bytes / %d files, want %d / %d", workers, got.SizeBytes, got.Files, wantSize, wantFiles)
		}
	}
	if wantFiles != 5 {
		t.Fatalf("reference counted %d files, want 5", wantFiles)
	}
}

func TestDirSizeNewestModTime(t *testing.T) {
	root := sampleTree(t)
	ageTree(t, root)
	stamp := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	f := filepath.Join(root, "a", "b", "f2")
	if err := os.Chtimes(f, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	got := mustSize(t, root, Options{})
	if !got.NewestModTime.Equal(stamp) {
		t.Errorf("newest = %v, want %v", got.NewestModTime, stamp)
	}
}

func TestDirSizeSpecialPaths(t *testing.T) {
	root := sampleTree(t)
	t.Run("nonexistent", func(t *testing.T) {
		if _, err := DirSize(context.Background(), filepath.Join(root, "nope"), Options{}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("regular file", func(t *testing.T) {
		got := mustSize(t, filepath.Join(root, "top"), Options{})
		e, _ := Stat(filepath.Join(root, "top"))
		if got.Files != 1 || got.SizeBytes != e.Allocated || got.NewestModTime.IsZero() {
			t.Errorf("got %+v, want own size %d", got, e.Allocated)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(root, "lnk")
		mkSymlink(t, filepath.Join(root, "a"), link)
		e, _ := Stat(link)
		got := mustSize(t, link, Options{})
		if got.Files != 1 || got.SizeBytes != e.Size {
			t.Errorf("got %+v, want own size %d and one file", got, e.Size)
		}
	})
	t.Run("empty directory", func(t *testing.T) {
		got := mustSize(t, filepath.Join(root, "e"), Options{})
		if got.SizeBytes != 0 || got.Files != 0 || !got.NewestModTime.IsZero() {
			t.Errorf("got %+v", got)
		}
	})
}

func TestDirSizeSymlinksCountOwnSizeAndAreNotFollowed(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "big"), 1<<20)
	root := sampleTree(t)
	base := mustSize(t, root, Options{})
	link := filepath.Join(root, "lnk")
	mkSymlink(t, outside, link)
	e, _ := Stat(link)

	got := mustSize(t, root, Options{})
	if got.SizeBytes != base.SizeBytes+e.Size || got.Files != base.Files {
		t.Errorf("got %+v, base %+v, link size %d", got, base, e.Size)
	}
}

func TestDirSizeCountsHardLinksOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard link dedupe is unix only; Windows sizes are logical")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "one", "f"), 3000)
	writeFile(t, filepath.Join(root, "two", "other"), 10)
	if err := os.Link(filepath.Join(root, "one", "f"), filepath.Join(root, "two", "f2")); err != nil {
		t.Skipf("hard links unsupported: %v", err)
	}
	if err := os.Link(filepath.Join(root, "one", "f"), filepath.Join(root, "f3")); err != nil {
		t.Fatal(err)
	}
	f, _ := Stat(filepath.Join(root, "one", "f"))
	o, _ := Stat(filepath.Join(root, "two", "other"))
	want := f.Allocated + o.Allocated

	opts := Options{CacheDir: t.TempDir()}
	ageTree(t, root)
	for _, name := range []string{"cold", "warm"} {
		got := mustSize(t, root, opts)
		if got.SizeBytes != want || got.Files != 2 {
			t.Errorf("%s: got %+v, want %d bytes / 2 files", name, got, want)
		}
	}
}

func TestDirSizeCancelled(t *testing.T) {
	root := sampleTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := runtime.NumGoroutine()
	if _, err := DirSize(ctx, root, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertNoGoroutineLeak(t, before)
}

// TestDirSizeIncomplete proves that a directory the sizer cannot list marks
// the summary Incomplete (its bytes are missing from SizeBytes), with and
// without the cache, and that the flag disappears once the tree is readable.
// The failure is injected through the listDir seam so the test also runs as
// root and on Windows, where chmod cannot deny access.
func TestDirSizeIncomplete(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "ok", "f"), 4096)
	writeFile(t, filepath.Join(root, "locked", "big"), 8192)
	ageTree(t, root)
	locked := filepath.Join(root, "locked")

	orig := listDir
	deny := true
	listDir = func(dir string) ([]os.DirEntry, error) {
		if deny && dir == locked {
			return nil, &os.PathError{Op: "open", Path: dir, Err: os.ErrPermission}
		}
		return orig(dir)
	}
	t.Cleanup(func() { listDir = orig })

	for _, name := range []string{"cold", "warm"} {
		sum, err := DirSize(context.Background(), root, Options{CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		if !sum.Incomplete {
			t.Errorf("%s: Incomplete = false for a tree with an unreadable directory", name)
		}
	}
	deny = false
	sum, err := DirSize(context.Background(), root, Options{CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Incomplete {
		t.Error("Incomplete stayed set after the directory became readable")
	}
}

// TestDirSizeIncompleteSurvivesCache pins that the flag is part of the cache
// record layout, so a warm read cannot silently turn a partial size complete.
func TestDirSizeIncompleteSurvivesCache(t *testing.T) {
	rec := &dirRecord{Incomplete: true, Subdirs: []string{}}
	file := filepath.Join(t.TempDir(), "c.json")
	if err := storeCache(file, "/x", map[string]*dirRecord{"": rec}); err != nil {
		t.Fatal(err)
	}
	got := loadCache(file, "/x")
	if got == nil || !got[""].Incomplete {
		t.Fatalf("Incomplete lost in the cache round trip: %+v", got)
	}
}
