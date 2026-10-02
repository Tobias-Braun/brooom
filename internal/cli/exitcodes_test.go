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
		{"missing path for sweep", []string{"sweep", "tidy", "extra"}, ExitUsage, `path "extra"`},
		{"two paths for sweep", []string{"sweep", "tidy", "a", "b"}, ExitUsage, "accepts at most 2 arg"},
		{"removed command", []string{"roots"}, ExitUsage, "unknown command"},
		{"undo too many", []string{"undo", "a", "b"}, ExitUsage, "accepts at most 1 arg"},
		{"sessions takes no argument", []string{"sessions", "a"}, ExitUsage, "unknown command"},
		{"config typo", []string{"config", "bogus"}, ExitUsage, "unknown command"},
		{"config show extra", []string{"config", "show", "x"}, ExitUsage, "unknown command"},
		{"config bare prints help", []string{"config"}, ExitOK, ""},
		{"bare brooom prints help", nil, ExitOK, ""},
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
