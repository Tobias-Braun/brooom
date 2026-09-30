package walk

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWalkReportsEverythingBelowRoot(t *testing.T) {
	root := sampleTree(t)
	c := newCollected()
	if err := Walk(context.Background(), root, Options{Concurrency: 4}, c.visit, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "a/b", "a/b/c", "a/b/c/f3", "a/b/f2", "a/f1", "d", "d/f4", "e", "top"}
	if got := c.rels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rels = %v, want %v", got, want)
	}
	f3 := c.entries["a/b/c/f3"]
	if f3.Depth != 4 || c.entries["a"].Depth != 1 {
		t.Errorf("depths: f3=%d a=%d", f3.Depth, c.entries["a"].Depth)
	}
	if f3.Path != filepath.Join(root, "a", "b", "c", "f3") || f3.Name != "f3" {
		t.Errorf("path/name = %q %q", f3.Path, f3.Name)
	}
	if f3.Size != 1 || f3.Allocated < f3.Size || f3.ModTime.IsZero() {
		t.Errorf("sizes: %+v", f3)
	}
	if !c.entries["a"].IsDir() || c.entries["a"].Size != 0 {
		t.Errorf("dir entry: %+v", c.entries["a"])
	}
}

func TestWalkRootErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, file, 1)
	target := filepath.Join(dir, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	mkSymlink(t, target, link)

	for name, root := range map[string]string{
		"regular file":     file,
		"missing":          filepath.Join(dir, "nope"),
		"symlink to a dir": link,
	} {
		t.Run(name, func(t *testing.T) {
			err := Walk(context.Background(), root, Options{}, func(Entry) Decision { return Continue }, nil)
			if err == nil || !strings.Contains(err.Error(), filepath.Base(root)) {
				t.Fatalf("err = %v, want error naming %s", err, root)
			}
		})
	}
}

func TestWalkRelativeRootBecomesAbsolute(t *testing.T) {
	root := sampleTree(t)
	t.Chdir(root)
	c := newCollected()
	if err := Walk(context.Background(), ".", Options{}, c.visit, nil); err != nil {
		t.Fatal(err)
	}
	if e := c.entries["top"]; !filepath.IsAbs(e.Path) {
		t.Errorf("path %q is not absolute", e.Path)
	}
}

func TestWalkSkipDirAndSkipNames(t *testing.T) {
	root := sampleTree(t)
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "x.js"), 5)
	writeFile(t, filepath.Join(root, ".git", "HEAD"), 5)
	writeFile(t, filepath.Join(root, "sub", "Cache", "y"), 5)

	tests := []struct {
		name    string
		opts    Options
		skipRel string
		absent  []string
		present []string
	}{
		{"builtin .git", Options{}, "", []string{".git/HEAD"}, []string{".git", "node_modules/pkg/x.js"}},
		{"SkipNames", Options{SkipNames: []string{"node_modules"}}, "", []string{"node_modules/pkg", ".git/HEAD"}, []string{"node_modules", "a/b"}},
		{"visit SkipDir", Options{}, "a", []string{"a/f1", "a/b"}, []string{"a", "d/f4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCollected()
			visit := func(e Entry) Decision {
				c.visit(e)
				if e.Rel == tt.skipRel {
					return SkipDir
				}
				return Continue
			}
			if err := Walk(context.Background(), root, tt.opts, visit, nil); err != nil {
				t.Fatal(err)
			}
			for _, r := range tt.absent {
				if _, ok := c.entries[r]; ok {
					t.Errorf("%s must not be visited", r)
				}
			}
			for _, r := range tt.present {
				if _, ok := c.entries[r]; !ok {
					t.Errorf("%s must be visited", r)
				}
			}
		})
	}
}

func TestWalkSkipNamesCaseSensitivity(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Node_Modules", "x"), 1)
	c := newCollected()
	if err := Walk(context.Background(), root, Options{SkipNames: []string{"node_modules"}}, c.visit, nil); err != nil {
		t.Fatal(err)
	}
	_, descended := c.entries["Node_Modules/x"]
	wantDescended := !foldNames()
	if descended != wantDescended {
		t.Errorf("descended = %v on %s, want %v", descended, runtime.GOOS, wantDescended)
	}
}

func TestWalkMaxDepth(t *testing.T) {
	root := sampleTree(t)
	tests := []struct {
		max  int
		want []string
	}{
		{1, []string{"a", "d", "e", "top"}},
		{2, []string{"a", "a/b", "a/f1", "d", "d/f4", "e", "top"}},
		{0, []string{"a", "a/b", "a/b/c", "a/b/c/f3", "a/b/f2", "a/f1", "d", "d/f4", "e", "top"}},
	}
	for _, tt := range tests {
		c := newCollected()
		if err := Walk(context.Background(), root, Options{MaxDepth: tt.max}, c.visit, nil); err != nil {
			t.Fatal(err)
		}
		if got := c.rels(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("MaxDepth %d: got %v, want %v", tt.max, got, tt.want)
		}
	}
}

func TestWalkDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret", "s.txt"), 5)
	root := sampleTree(t)
	mkSymlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "link"))

	c := newCollected()
	if err := Walk(context.Background(), root, Options{}, c.visit, nil); err != nil {
		t.Fatal(err)
	}
	l, ok := c.entries["link"]
	if !ok || !l.IsSymlink() || l.IsDir() {
		t.Fatalf("link entry = %+v (found %v)", l, ok)
	}
	for r := range c.entries {
		if strings.HasPrefix(r, "link/") {
			t.Errorf("descended through symlink: %s", r)
		}
	}
}

func TestWalkUnreadableDirectory(t *testing.T) {
	skipIfNoChmod(t)
	root := sampleTree(t)
	locked := filepath.Join(root, "d")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	var mu sync.Mutex
	var errPaths []string
	c := newCollected()
	err := Walk(context.Background(), root, Options{}, c.visit, func(p string, err error) {
		mu.Lock()
		errPaths = append(errPaths, p)
		mu.Unlock()
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("err = %v, want permission error", err)
		}
	})
	if err != nil {
		t.Fatalf("walk aborted: %v", err)
	}
	if len(errPaths) != 1 || errPaths[0] != locked {
		t.Errorf("onErr paths = %v, want [%s]", errPaths, locked)
	}
	if _, ok := c.entries["a/b/f2"]; !ok {
		t.Error("siblings of the unreadable directory were not walked")
	}
}

func TestWalkCancellation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		writeFile(t, filepath.Join(root, "d"+string(rune('a'+i%26)), "x"+string(rune('a'+i/26)), "f"), 1)
	}
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var visited int
	var mu sync.Mutex
	err := Walk(ctx, root, Options{Concurrency: 4}, func(Entry) Decision {
		mu.Lock()
		visited++
		mu.Unlock()
		cancel()
		return Continue
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertNoGoroutineLeak(t, before)
}

func TestWalkAlreadyCancelled(t *testing.T) {
	root := sampleTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := Walk(ctx, root, Options{}, func(Entry) Decision { called = true; return Continue }, nil)
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("err = %v, visit called = %v", err, called)
	}
}

// assertNoGoroutineLeak waits briefly for goroutines started by the test to
// exit and fails if more remain than before.
func assertNoGoroutineLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("goroutines: %d before, %d after", before, n)
	}
}

// A single worker on a tree that is both deep and wide proves the unbounded
// queue cannot deadlock the pool.
func TestWalkDeepAndWideWithOneWorker(t *testing.T) {
	root := t.TempDir()
	deep := root
	for i := 0; i < 60; i++ {
		deep = filepath.Join(deep, "n")
	}
	for i := 0; i < 300; i++ {
		if err := os.MkdirAll(filepath.Join(root, "w", string(rune('A'+i%26)), strings.Repeat("x", i/26+1)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 2} {
		var n int
		var mu sync.Mutex
		done := make(chan error, 1)
		go func() {
			done <- Walk(context.Background(), root, Options{Concurrency: workers}, func(Entry) Decision {
				mu.Lock()
				n++
				mu.Unlock()
				return Continue
			}, nil)
		}()
		select {
		case err := <-done:
			if err != nil || n < 300 {
				t.Errorf("workers=%d: err=%v entries=%d", workers, err, n)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("workers=%d: walk did not finish", workers)
		}
	}
}

func TestWalkListingRaces(t *testing.T) {
	root := sampleTree(t)
	orig := listDir
	t.Cleanup(func() { listDir = orig })
	listDir = func(dir string) ([]os.DirEntry, error) {
		des, err := orig(dir)
		if dir != root {
			return des, err
		}
		return append(des,
			fakeEntry{name: "ghost", err: fs.ErrNotExist},
			fakeEntry{name: "broken", err: errors.New("boom")},
		), err
	}
	var errPaths []string
	c := newCollected()
	if err := Walk(context.Background(), root, Options{Concurrency: 1}, c.visit, func(p string, _ error) {
		errPaths = append(errPaths, filepath.Base(p))
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(errPaths, []string{"broken"}) {
		t.Errorf("onErr = %v, want only the non-vanished failure", errPaths)
	}
	if _, ok := c.entries["ghost"]; ok {
		t.Error("vanished entry was reported")
	}
}

// fakeEntry is a DirEntry whose Info fails.
type fakeEntry struct {
	name string
	err  error
}

func (f fakeEntry) Name() string               { return f.name }
func (f fakeEntry) IsDir() bool                { return false }
func (f fakeEntry) Type() fs.FileMode          { return 0 }
func (f fakeEntry) Info() (fs.FileInfo, error) { return nil, f.err }

func TestStat(t *testing.T) {
	root := sampleTree(t)
	e, err := Stat(filepath.Join(root, "top"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Rel != "" || e.Depth != 0 || e.Size != 10 || e.Name != "top" || !e.Type.IsRegular() || e.Allocated < 10 {
		t.Errorf("file entry: %+v", e)
	}
	d, err := Stat(filepath.Join(root, "a"))
	if err != nil || !d.IsDir() {
		t.Errorf("dir entry: %+v %v", d, err)
	}
	if _, err := Stat(filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: err = %v", err)
	}

	t.Run("symlink is not followed", func(t *testing.T) {
		link := filepath.Join(root, "l")
		mkSymlink(t, filepath.Join(root, "a"), link)
		l, err := Stat(link)
		if err != nil || !l.IsSymlink() || l.IsDir() {
			t.Errorf("link entry: %+v %v", l, err)
		}
	})
}

func TestParallelWalkEqualsSerialReference(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		for j := 0; j < 5; j++ {
			writeFile(t, filepath.Join(root, "d"+string(rune('A'+i%26)), "s"+string(rune('a'+i/26)), "f"+string(rune('0'+j))), i*j)
		}
	}
	want := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if p != root {
			rel, _ := filepath.Rel(root, p)
			want[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	c := newCollected()
	if err := Walk(context.Background(), root, Options{Concurrency: 8}, c.visit, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for r := range c.entries {
		got[r] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parallel walk differs from serial: %d vs %d entries", len(got), len(want))
	}
}
