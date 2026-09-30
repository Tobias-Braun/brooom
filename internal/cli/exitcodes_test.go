package cli

import (
	"strings"
	"testing"
)

// TestUsageErrorsExitTwo reproduces #112: wrong invocations exited 1 or, for
// unknown subcommands of group commands, printed help and exited 0.
func TestUsageErrorsExitTwo(t *testing.T) {
	isolate(t)
	tests := []struct {
		name string
		args []string
		want int
		msg  string
	}{
		{"unknown top-level command", []string{"foo"}, ExitUsage, "unknown command"},
		{"extra arg to scan", []string{"scan", "extra"}, ExitUsage, "unknown command"},
		{"roots add without path", []string{"roots", "add"}, ExitUsage, "requires at least 1 arg"},
		{"roots remove without path", []string{"roots", "remove"}, ExitUsage, "requires at least 1 arg"},
		{"undo too many", []string{"undo", "a", "b"}, ExitUsage, "accepts at most 1 arg"},
		{"sessions too many", []string{"sessions", "a", "b"}, ExitUsage, "accepts at most 1 arg"},
		{"config typo", []string{"config", "bogus"}, ExitUsage, "unknown command"},
		{"roots typo", []string{"roots", "lst"}, ExitUsage, "unknown command"},
		{"roots ad with arg", []string{"roots", "ad", "~/dev"}, ExitUsage, "unknown command"},
		{"git typo", []string{"git", "bogus"}, ExitUsage, "unknown command"},
		{"config show extra", []string{"config", "show", "x"}, ExitUsage, "unknown command"},
		{"config bare prints help", []string{"config"}, ExitOK, ""},
		{"roots bare prints help", []string{"roots"}, ExitOK, ""},
		{"git bare prints help", []string{"git"}, ExitOK, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := brooom(t, "", tc.args...)
			if code != tc.want {
				t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, tc.want, out, errOut)
			}
			if tc.msg != "" && !strings.Contains(errOut, tc.msg) {
				t.Errorf("stderr %q lacks %q", errOut, tc.msg)
			}
			if tc.want == ExitOK && !strings.Contains(out, "Usage:") {
				t.Errorf("bare group did not print help: %q", out)
			}
		})
	}
}
