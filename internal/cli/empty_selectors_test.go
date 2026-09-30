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
	dir, file := junkDir(t, f.repo.Dir, "node_modules")
	report := writeReportFile(t, trashFinding(f.repo.Dir, dir))

	tests := []struct {
		name string
		args []string
		flag string
	}{
		{"id empty", []string{"clean", "--from", report, "--id", ""}, "--id"},
		{"id comma only", []string{"clean", "--from", report, "--id", ","}, "--id"},
		{"id empty element", []string{"clean", "--from", report, "--id", "abc,,def"}, "--id"},
		{"id blank", []string{"clean", "--from", report, "--id", " "}, "--id"},
		{"path empty", []string{"clean", "--from", report, "--path", ""}, "--path"},
		{"path blank", []string{"undo", "--path", " "}, "--path"},
		{"detector empty", []string{"sweep", "-d", ""}, "--detector"},
		{"detector empty element", []string{"sweep", "-d", "build-artifacts,"}, "--detector"},
		{"detector on clean", []string{"clean", "--from", report, "-d", ""}, "--detector"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string{}, tt.args...), "--yes")
			args = append(args, quarantine...)
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
	f := newCleanupFixture(t, nil)
	dir, _ := junkDir(t, f.repo.Dir, "node_modules")
	report := writeReportFile(t, trashFinding(f.repo.Dir, dir))
	id := trashFinding(f.repo.Dir, dir).ID
	for _, args := range [][]string{
		{"clean", "--from", report, "--id", id, "--dry-run"},
		{"clean", "--from", report, "--id", id + "," + id, "--dry-run"},
		{"clean", "--from", report, "--id", id, "--id", id, "--dry-run"},
		{"scan", "-d", "build-artifacts,worktrees"},
	} {
		code, out, errOut := brooom(t, "", args...)
		if code == ExitUsage {
			t.Errorf("%v rejected: %s%s", args, out, errOut)
		}
	}
}
