//go:build linux

package procs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeProc builds a procfs look-alike: fds maps "pid/fd" to the symlink
// target, and "self-owned" or non numeric entries exercise the filters.
func fakeProc(t *testing.T, fds map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	for key, target := range fds {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, key)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, key)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanProcFakeTree(t *testing.T) {
	fds := map[string]string{
		"100/fd/3":  "/var/log/app.log",
		"100/fd/4":  "socket:[12345]",
		"101/fd/0":  "anon_inode:[eventpoll]",
		"101/fd/7":  "/var/log/gone.log (deleted)",
		"102/fd/5":  "/data/dir/sub/deep.log",
		"102/fd/6":  "/data/dirextra/file",
		"self/fd/1": "/var/log/ignored-nonnumeric.log",
	}
	root := fakeProc(t, fds)
	// A process whose fd directory is missing (exited or unreadable) must be
	// skipped silently.
	if err := os.MkdirAll(filepath.Join(root, "103"), 0o755); err != nil {
		t.Fatal(err)
	}

	files := []string{"/var/log/app.log", "/var/log/gone.log", "/var/log/ignored-nonnumeric.log", "/var/log/APP.LOG"}
	dirs := []string{"/data/dir", "/data/other", "/data"}
	res := map[string]bool{}
	for _, p := range append(append([]string{}, files...), dirs...) {
		res[p] = false
	}
	if err := scanProc(context.Background(), root, files, dirs, res); err != nil {
		t.Fatalf("scanProc: %v", err)
	}
	want := map[string]bool{
		"/var/log/app.log":                true,
		"/var/log/gone.log":               false, // deleted file
		"/var/log/ignored-nonnumeric.log": false, // not a pid directory
		"/var/log/APP.LOG":                false, // paths are case-sensitive
		"/data/dir":                       true,
		"/data/other":                     false,
		"/data":                           true, // both 102 targets are below it
	}
	for p, w := range want {
		if res[p] != w {
			t.Errorf("%s: got %v, want %v", p, res[p], w)
		}
	}
}

func TestScanProcMissingRootIsUnavailable(t *testing.T) {
	err := scanProc(context.Background(), filepath.Join(t.TempDir(), "no-proc"), []string{"/a"}, nil, map[string]bool{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v, want ErrUnavailable", err)
	}
}

func TestScanProcCancelledIsIncomplete(t *testing.T) {
	root := fakeProc(t, map[string]string{"1/fd/0": "/a"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := scanProc(ctx, root, []string{"/a"}, nil, map[string]bool{"/a": false})
	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("got %v, want ErrIncomplete", err)
	}
}

func TestOpenFilesSeesOwnProcess(t *testing.T) {
	// The real /proc must include the test process itself.
	file := filepath.Join(resolvedTemp(t), "own.log")
	writeFile(t, file)
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skipf("no readable /proc: %v", err)
	}
	if !query(t, file)[file] {
		t.Error("own open file not found")
	}
}
