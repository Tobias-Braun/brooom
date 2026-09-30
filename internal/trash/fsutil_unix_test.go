//go:build unix

package trash

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
)

// A fifo inside a tree must make the copy fail loudly instead of hanging or
// silently skipping it, and the cross-device move must then leave the source
// intact with no partial destination.
func TestCopyRefusesSpecialFiles(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a"), "a", 0o644)
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}
	failCrossDevice(t)
	dst := filepath.Join(root, "dst")
	if err := moveTree(context.Background(), src, dst); err == nil {
		t.Fatal("special file was not refused")
	}
	if !exists(filepath.Join(src, "a")) || !exists(filepath.Join(src, "pipe")) {
		t.Error("source was touched")
	}
	if exists(dst) {
		t.Error("partial destination left behind")
	}
}
