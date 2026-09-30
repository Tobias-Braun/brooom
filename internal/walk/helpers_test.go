package walk

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeFile creates a file of n bytes (and its parent directories).
func writeFile(t testing.TB, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkSymlink creates a symlink or skips the test when the OS refuses (Windows
// without the symlink privilege).
func mkSymlink(t testing.TB, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
}

// sampleTree builds a small deterministic tree and returns its root.
//
//	a/f1 (100)  a/b/f2 (5000)  a/b/c/f3 (1)  d/f4 (9000)  e/ (empty)  top (10)
func sampleTree(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "f1"), 100)
	writeFile(t, filepath.Join(root, "a", "b", "f2"), 5000)
	writeFile(t, filepath.Join(root, "a", "b", "c", "f3"), 1)
	writeFile(t, filepath.Join(root, "d", "f4"), 9000)
	writeFile(t, filepath.Join(root, "top"), 10)
	if err := os.MkdirAll(filepath.Join(root, "e"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// ageTree sets every mtime below root (and the directories themselves) one
// hour into the past so cached directory records are not racy.
func ageTree(t testing.TB, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		return os.Chtimes(p, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Deepest first, so touching a child never bumps an already aged parent.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// referenceSize is a serial, cache-free reference for trees without hard
// links: allocated bytes of regular files plus the file count.
func referenceSize(t testing.TB, root string) (int64, int) {
	t.Helper()
	var size int64
	var files int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		e, err := Stat(p)
		if err != nil {
			return err
		}
		// Directories count their own blocks (like du), files their
		// allocation; only files are counted as files.
		size += e.Allocated
		if !d.IsDir() {
			files++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return size, files
}

// countReads counts directory reads for the duration of the test.
func countReads(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	orig := listDir
	listDir = func(dir string) ([]os.DirEntry, error) {
		n.Add(1)
		return orig(dir)
	}
	t.Cleanup(func() { listDir = orig })
	return &n
}

// collected gathers visited entries from concurrent visit calls.
type collected struct {
	mu      sync.Mutex
	entries map[string]Entry // by Rel
}

func newCollected() *collected { return &collected{entries: map[string]Entry{}} }

func (c *collected) visit(e Entry) Decision {
	c.mu.Lock()
	c.entries[e.Rel] = e
	c.mu.Unlock()
	return Continue
}

func (c *collected) rels() []string {
	var out []string
	for r := range c.entries {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// skipIfRootOrWindows skips permission based tests where chmod cannot deny
// access.
func skipIfNoChmod(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not deny access on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
}
