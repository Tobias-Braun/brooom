package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// TestRunMaintUsesLongerTimeout: maintenance used to share the scan bound, so
// a long git gc --prune was killed at the default timeout. A fake git that
// outlasts the runner's short bound stands in for a slow gc.
func TestRunMaintUsesLongerTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as the fake git")
	}
	script := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &gitx.ExecRunner{Path: script, Timeout: 150 * time.Millisecond}
	env := &Env{Git: runner}
	repo := &gitx.Repo{Dir: t.TempDir()}

	// Sanity: the scan-style bound really would kill this command.
	if _, err := runner.Run(context.Background(), repo.Dir, "gc"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("plain run: err = %v, want a timeout", err)
	}
	if err := runMaint(context.Background(), env, repo, "gc", "--prune=now"); err != nil {
		t.Errorf("maintenance was cut off by the scan timeout: %v", err)
	}
}
