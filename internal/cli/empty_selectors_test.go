package cli

import (
	"strings"
	"testing"
)

// TestEmptySelectorsAreUsageErrors covers #198: a selector flag that is given
// but names nothing must fail instead of widening to everything, and nothing
// may be touched.
func TestEmptySelectorsAreUsageErrors(t *testing.T) {
	f := newCleanupFixture(t, nil)
	file := oldJunk(t, f.repo.Dir)

	tests := []struct {
		name string
		args []string
		flag string
	}{
		{"path blank", []string{"undo", "--path", " "}, "--path"},
		{"path empty", []string{"undo", "--path", ""}, "--path"},
		{"detector empty", []string{"sweep", "tidy", "-d", ""}, "--detector"},
		{"detector comma only", []string{"sweep", "tidy", "-d", ","}, "--detector"},
		{"detector empty element", []string{"sweep", "tidy", "-d", "log-and-runtime-files,"}, "--detector"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string{}, tt.args...), "--yes")
			code, out, errOut := brooom(t, "", args...)
			if code != ExitUsage || !strings.Contains(errOut, tt.flag) {
				t.Fatalf("code %d, want usage error naming %s\nstdout: %s\nstderr: %s", code, tt.flag, out, errOut)
			}
			if !exists(file) || len(f.sessions()) != 0 {
				t.Fatal("a rejected invocation changed something")
			}
		})
	}
}

// TestNonEmptySelectorsStillWork makes sure the check does not reject valid
// spellings: repeated flags, comma lists and a leading space.
func TestNonEmptySelectorsStillWork(t *testing.T) {
	newCleanupFixture(t, nil)
	for _, args := range [][]string{
		{"sweep", "-d", "build-artifacts,worktrees", "--dry-run"},
		{"sweep", "-d", "build-artifacts", "-d", "worktrees", "--dry-run"},
		{"sweep", "-d", " worktrees", "--dry-run"},
	} {
		code, out, errOut := brooom(t, "", args...)
		if code == ExitUsage {
			t.Errorf("%v rejected: %s%s", args, out, errOut)
		}
	}
}
