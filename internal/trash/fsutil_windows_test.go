//go:build windows

package trash

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// makeJunction creates a mount-point junction link -> target with mklink /J,
// which needs no privilege. The test is skipped if that fails.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create junction: %v: %s", err, out)
	}
}

// TestCheckCopyableRefusesJunction covers issue #123: since Go 1.23 a
// junction is ModeIrregular and used to fail copyTree half way through a
// pnpm/npm workspace copy. It is now refused before anything is copied, with
// a message that names it as a junction.
func TestCheckCopyableRefusesJunction(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	writeFile(t, filepath.Join(target, "t.txt"), "t", 0o644)
	src := filepath.Join(root, "ws")
	writeFile(t, filepath.Join(src, "a.txt"), "a", 0o644)
	junction := filepath.Join(src, "node_modules")
	makeJunction(t, junction, target)

	err := checkCopyable(src)
	if err == nil {
		t.Fatal("junction accepted")
	}
	for _, want := range []string{junction, "junction", "nothing was copied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	failCrossDevice(t)
	dst := filepath.Join(root, "dst")
	if err := moveTree(context.Background(), src, dst); err == nil {
		t.Fatal("move with a junction succeeded")
	}
	if exists(dst) {
		t.Error("destination was created before the refusal")
	}
	if !exists(filepath.Join(target, "t.txt")) || !exists(filepath.Join(src, "a.txt")) {
		t.Error("the refused move touched source or junction target")
	}
}

// TestCopyTreeDirectorySymlinkKeepsDirectoryFlag covers the symlink flag: a
// relative link to a directory that is copied after the link must still be a
// directory link, which os.Symlink got wrong by statting the unfinished copy.
func TestCopyTreeDirectorySymlinkKeepsDirectoryFlag(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	// "a-link" sorts before "z-dir", so the link is created before its target.
	writeFile(t, filepath.Join(src, "z-dir", "f.txt"), "f", 0o644)
	if err := os.Symlink("z-dir", filepath.Join(src, "a-link")); err != nil {
		t.Skipf("cannot create symlinks (needs Developer Mode or elevation): %v", err)
	}
	dst := filepath.Join(root, "dst")
	if err := copyTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	attrs, err := linkAttrs(filepath.Join(dst, "a-link"))
	if err != nil {
		t.Fatal(err)
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		t.Error("the copied directory link became a file link")
	}
	if _, err := os.Stat(filepath.Join(dst, "a-link", "f.txt")); err != nil {
		t.Errorf("the copied link does not resolve into the copied directory: %v", err)
	}
	if err := verifyCopy(src, dst); err != nil {
		t.Errorf("verifyCopy: %v", err)
	}
}

// TestIsLockedError checks the Win32 codes that mean "in use".
func TestIsLockedError(t *testing.T) {
	for _, code := range []syscall.Errno{5, 32, 33} {
		if !isLockedError(&os.PathError{Op: "remove", Path: `C:\x`, Err: code}) {
			t.Errorf("errno %d not treated as locked", code)
		}
	}
	if isLockedError(&os.PathError{Op: "remove", Path: `C:\x`, Err: syscall.Errno(2)}) {
		t.Error("file-not-found treated as locked")
	}
}
