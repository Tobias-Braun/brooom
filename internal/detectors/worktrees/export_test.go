package worktrees

import (
	"context"
	"testing"
)

// SetOpenFiles replaces the open-file check for one test, so in-use handling
// can be simulated (including an unavailable check) on every OS.
func SetOpenFiles(t *testing.T, fn func(context.Context, []string) (map[string]bool, error)) {
	t.Helper()
	old := openFiles
	openFiles = fn
	t.Cleanup(func() { openFiles = old })
}
