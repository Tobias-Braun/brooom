//go:build unix

package trash

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestCheckCopyableRefusesSpecialFileUpFront checks that an entry copyTree
// cannot reproduce is found by the pre-copy walk, so the refusal names it and
// says nothing was copied (issue #123: junctions failed half way through).
func TestCheckCopyableRefusesSpecialFileUpFront(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a"), "a", 0o644)
	pipe := filepath.Join(src, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}
	err := checkCopyable(src)
	if err == nil {
		t.Fatal("special file accepted")
	}
	for _, want := range []string{pipe, "nothing was copied", "BROOOM_HOME"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	failCrossDevice(t)
	dst := filepath.Join(root, "dst")
	if err := moveTree(context.Background(), src, dst); err == nil || !strings.Contains(err.Error(), "nothing was copied") {
		t.Fatalf("moveTree err = %v, want the pre-copy refusal", err)
	}
	if exists(dst) {
		t.Error("destination was created before the refusal")
	}
}
