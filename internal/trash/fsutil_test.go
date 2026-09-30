package trash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/walk"
)

// failCrossDevice makes every rename fail like a move across filesystems, so
// moveTree takes its copy + verify + delete path.
func failCrossDevice(t *testing.T) {
	t.Helper()
	orig := renameFunc
	renameFunc = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: crossDeviceErrno}
	}
	t.Cleanup(func() { renameFunc = orig })
}

// symlinkOrSkip creates a symlink, skipping the test where the platform or
// its permissions do not allow it (Windows without developer mode).
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create symlinks on this Windows setup: %v", err)
		}
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestIsCrossDevice(t *testing.T) {
	wrapped := &os.LinkError{Op: "rename", Err: crossDeviceErrno}
	if !isCrossDevice(wrapped) {
		t.Error("LinkError wrapping the cross-device errno not detected")
	}
	if isCrossDevice(errors.New("other")) || isCrossDevice(os.ErrPermission) {
		t.Error("unrelated errors reported as cross-device")
	}
}

func TestCopyTreePreservesModesAndTimes(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), "hello", 0o640)
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "world!", 0o600)
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, p := range []string{filepath.Join(src, "a.txt"), filepath.Join(src, "sub", "b.txt"), filepath.Join(src, "sub"), src} {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(src, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(src, "sub"), mtime, mtime); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(root, "dst")
	if err := copyTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if err := verifyCopy(src, dst); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"a.txt", filepath.Join("sub", "b.txt"), "sub", "."} {
		s, _ := os.Lstat(filepath.Join(src, rel))
		d, err := os.Lstat(filepath.Join(dst, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !d.ModTime().Equal(mtime) {
			t.Errorf("%s: mtime %v, want %v", rel, d.ModTime(), mtime)
		}
		if runtime.GOOS != "windows" && s.Mode().Perm() != d.Mode().Perm() {
			t.Errorf("%s: mode %v, want %v", rel, d.Mode().Perm(), s.Mode().Perm())
		}
	}
}

func TestCopyTreeKeepsSymlinksAsLinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(root, "outside", "f.txt"), "x", 0o644)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(root, "outside"), filepath.Join(src, "dirlink"))
	symlinkOrSkip(t, filepath.Join(root, "missing"), filepath.Join(src, "dangling"))

	dst := filepath.Join(root, "dst")
	if err := copyTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dirlink", "dangling"} {
		fi, err := os.Lstat(filepath.Join(dst, name))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s not copied as a symlink: %v %v", name, fi, err)
		}
	}
	if err := verifyCopy(src, dst); err != nil {
		t.Fatal(err)
	}
}

func TestCopyTreeHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "src", "a"), "a", 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyTree(ctx, filepath.Join(root, "src"), filepath.Join(root, "dst")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestVerifyCopyDetectsDifferences(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "s", "f"), "abc", 0o644)
	writeFile(t, filepath.Join(root, "d", "f"), "abcd", 0o644)
	if err := verifyCopy(filepath.Join(root, "s"), filepath.Join(root, "d")); err == nil {
		t.Error("size mismatch not detected")
	}
	writeFile(t, filepath.Join(root, "d", "extra"), "", 0o644)
	if err := verifyCopy(filepath.Join(root, "s"), filepath.Join(root, "d")); err == nil {
		t.Error("entry count mismatch not detected")
	}
}

func TestTreeSize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "d", "a"), "12345", 0o644)
	writeFile(t, filepath.Join(root, "d", "n", "b"), "123", 0o644)
	writeFile(t, filepath.Join(root, "big"), "0123456789", 0o644)
	symlinkOrSkip(t, filepath.Join(root, "big"), filepath.Join(root, "d", "link"))
	// Allocated bytes with directory blocks; the symlink counts its own
	// length and its 10 byte (one block) target is not followed.
	d := filepath.Join(root, "d")
	if got, err := treeSize(d); err != nil || got != refSize(t, d) {
		t.Errorf("dir size = %d, %v; want %d", got, err, refSize(t, d))
	}
	fi, err := os.Lstat(filepath.Join(root, "big"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := treeSize(filepath.Join(root, "big")); got != walk.AllocatedSize(fi) {
		t.Errorf("file size = %d, want its allocation %d", got, walk.AllocatedSize(fi))
	}
	li, err := os.Lstat(filepath.Join(d, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := treeSize(filepath.Join(d, "link")); got != li.Size() {
		t.Errorf("symlink size = %d, want its own length %d", got, li.Size())
	}
}

func TestMoveTreeCrossDeviceFallback(t *testing.T) {
	failCrossDevice(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "f"), "data", 0o600)
	dst := filepath.Join(root, "dst")
	if err := moveTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if exists(src) {
		t.Error("source still exists after cross-device move")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "f")); string(b) != "data" {
		t.Errorf("content = %q", b)
	}
}

func TestMoveTreeRefusesExistingDestination(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a"), "a", 0o644)
	writeFile(t, filepath.Join(root, "b"), "b", 0o644)
	if err := moveTree(context.Background(), filepath.Join(root, "a"), filepath.Join(root, "b")); err == nil {
		t.Fatal("overwrite not refused")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b")); string(b) != "b" {
		t.Error("destination was modified")
	}
}

func TestMoveTreeCopyFailureLeavesSourceAndNoPartialDestination(t *testing.T) {
	failCrossDevice(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a"), "a", 0o644)
	writeFile(t, filepath.Join(src, "b"), "b", 0o644)
	// A cancelled context stops the copy after the destination was created.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst := filepath.Join(root, "dst")
	err := moveTree(ctx, src, dst)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !exists(filepath.Join(src, "a")) || !exists(filepath.Join(src, "b")) {
		t.Error("source was touched")
	}
	if exists(dst) {
		t.Error("partial destination left behind")
	}
}

// failSourceRemoval replaces the source removal primitive with one that
// deletes part of the tree and then fails, like RemoveAll hitting a locked
// file. Unlike permission tricks this also works when tests run as root.
func failSourceRemoval(t *testing.T, victim string) {
	t.Helper()
	orig := removeSourceFunc
	removeSourceFunc = func(path string) error {
		if err := os.Remove(filepath.Join(path, victim)); err != nil {
			return err
		}
		return errors.New("injected: file is locked")
	}
	t.Cleanup(func() { removeSourceFunc = orig })
}

func TestMoveTreeSourceRemovalFailureKeepsCopy(t *testing.T) {
	failCrossDevice(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a"), "a", 0o644)
	writeFile(t, filepath.Join(src, "b"), "b", 0o644)
	dst := filepath.Join(root, "dst")
	failSourceRemoval(t, "a")

	err := moveTree(context.Background(), src, dst)
	var partial *SourceNotRemovedError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want *SourceNotRemovedError", err)
	}
	failSourceRemovalCheck(t, err, src, dst)
}

// failSourceRemovalCheck asserts the complete data survives at dst and that
// the error names both locations.
func failSourceRemovalCheck(t *testing.T, err error, src, dst string) {
	t.Helper()
	for _, name := range []string{"a", "b"} {
		if b, rerr := os.ReadFile(filepath.Join(dst, name)); rerr != nil || string(b) != name {
			t.Errorf("copy lost %q: %v", name, rerr)
		}
	}
	for _, want := range []string{src, dst} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestIsWithin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "q")
	tests := []struct {
		p    string
		want bool
	}{
		{filepath.Join(root, "a"), true},
		{filepath.Join(root, "a", "b"), true},
		{root, false},
		{filepath.Join(root+"x", "a"), false},
		{filepath.Join(root, "..", "a"), false},
	}
	for _, tt := range tests {
		if got := isWithin(root, tt.p); got != tt.want {
			t.Errorf("isWithin(%q, %q) = %v, want %v", root, tt.p, got, tt.want)
		}
	}
}
