package gitx_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// TestCancelledRunIsInterruptionNotGitFailure: a git killed because the
// parent context was cancelled used to surface as *Error with exit code -1,
// which callers rendered as a failure advising --force.
func TestCancelledRunIsInterruptionNotGitFailure(t *testing.T) {
	r := &gitx.ExecRunner{Path: hangingScript(t, "git")}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := r.Run(ctx, t.TempDir(), "gc")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	var ge *gitx.Error
	if errors.As(err, &ge) {
		t.Errorf("cancellation must not be a *gitx.Error: %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cancellation must not look like a timeout: %v", err)
	}
}

// TestGitStartsInOwnProcessGroup: the child must not share the terminal's
// foreground process group, or Ctrl-C kills it even on uncancellable calls.
func TestGitStartsInOwnProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are checked through a POSIX shell and ps")
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not available")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nps -o pgid= -p $$\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := (&gitx.ExecRunner{Path: path}).Run(context.Background(), dir, "status")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Skip("ps failed: " + err.Error())
	}
	if got := strings.TrimSpace(out); got == "" || got == strings.TrimSpace(string(mine)) {
		t.Errorf("child pgid %q equals the test's group %q", got, mine)
	}
}
