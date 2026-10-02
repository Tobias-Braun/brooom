package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// complete runs `brooom __complete <args>` in-process and returns the
// candidate lines (without the trailing directive line) and the directive.
func complete(t *testing.T, args ...string) ([]string, string) {
	t.Helper()
	code, out, errOut := run(t, append([]string{"__complete"}, args...)...)
	if code != ExitOK {
		t.Fatalf("__complete %q: exit %d, stderr %q", args, code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	directive := lines[len(lines)-1]
	lines = lines[:len(lines)-1]
	return lines, directive
}

// values strips the descriptions from candidate lines.
func values(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i], _, _ = strings.Cut(l, "\t")
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func emptyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.HomeEnv, home)
	return home
}

// noFileComp is the directive line for cobra.ShellCompDirectiveNoFileComp.
const noFileComp = ":4"

func TestCompleteStaticFlagValues(t *testing.T) {
	emptyHome(t)
	tests := []struct {
		name string
		args []string
		want []string
		none []string
		desc string // substring expected in some description
	}{
		{"format", []string{"--format", ""}, []string{"json", "table", "plain"}, nil, ""},
		{"format prefix", []string{"--format", "nd"}, []string{"ndjson"}, []string{"json", "table"}, ""},
		{"preset", []string{"sweep", ""}, []string{"after-agents", "tidy", "everything"}, nil, "agent run"},
		{"preset prefix", []string{"sweep", "af"}, []string{"after-agents"}, []string{"tidy"}, ""},
		{"detector", []string{"--detector", ""}, []string{"merged-branch", "worktrees", "build-artifacts"}, nil, ""},
		{"detector short flag", []string{"-d", "wor"}, []string{"worktrees"}, []string{"merged-branch"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, directive := complete(t, tt.args...)
			if directive != noFileComp {
				t.Errorf("directive = %q, want %q", directive, noFileComp)
			}
			got := values(lines)
			for _, w := range tt.want {
				if !contains(got, w) {
					t.Errorf("missing %q in %q", w, got)
				}
			}
			for _, n := range tt.none {
				if contains(got, n) {
					t.Errorf("unexpected %q in %q", n, got)
				}
			}
			if tt.desc != "" && !strings.Contains(strings.Join(lines, "\n"), tt.desc) {
				t.Errorf("no description containing %q in %q", tt.desc, lines)
			}
		})
	}
}

func TestCompleteDetectorDescriptionsAndCommas(t *testing.T) {
	emptyHome(t)
	lines, _ := complete(t, "--detector", "")
	for _, l := range lines {
		if !strings.Contains(l, "\t") {
			t.Errorf("detector candidate %q has no description", l)
		}
	}

	lines, directive := complete(t, "--detector", "merged-branch,")
	got := values(lines)
	if directive != noFileComp {
		t.Errorf("directive = %q", directive)
	}
	if !contains(got, "merged-branch,worktrees") {
		t.Errorf("comma continuation missing: %q", got)
	}
	if contains(got, "merged-branch,merged-branch") {
		t.Errorf("already chosen detector offered again: %q", got)
	}

	lines, _ = complete(t, "--detector", "merged-branch,wor")
	if got := values(lines); len(got) != 1 || got[0] != "merged-branch,worktrees" {
		t.Errorf("prefix after comma: %q", got)
	}
}

func TestCompleteSessionIDs(t *testing.T) {
	store := sessionsHome(t)
	saveSession(t, store, "20260101-000000-aaaa", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true)
	saveSession(t, store, "20260301-000000-bbbb", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), true)

	lines, directive := complete(t, "undo", "")
	if directive != noFileComp {
		t.Errorf("directive = %q", directive)
	}
	if got := values(lines); len(got) != 2 || got[0] != "20260301-000000-bbbb" || got[1] != "20260101-000000-aaaa" {
		t.Fatalf("ids = %q, want newest first", got)
	}
	if !strings.Contains(lines[0], "sweep --yes") || !strings.Contains(lines[0], "1 applied") || !strings.Contains(lines[0], "2026-0") {
		t.Errorf("description lacks date or summary: %q", lines[0])
	}

	lines, _ = complete(t, "undo", "20260101")
	if got := values(lines); len(got) != 1 || got[0] != "20260101-000000-aaaa" {
		t.Errorf("prefix filter: %q", got)
	}
	// undo takes a single session id.
	if lines, _ := complete(t, "undo", "20260101-000000-aaaa", ""); len(values(lines)) != 0 {
		t.Errorf("second argument completed: %q", lines)
	}
}

func TestCompleteSessionIDsDegradeToEmpty(t *testing.T) {
	home := emptyHome(t)
	if lines, directive := complete(t, "undo", ""); len(lines) != 0 || directive != noFileComp {
		t.Errorf("no sessions dir: %q %q", lines, directive)
	}
	// A corrupt manifest must neither fail completion nor be offered.
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if lines, _ := complete(t, "undo", ""); len(lines) != 0 {
		t.Errorf("corrupt manifest offered: %q", lines)
	}
	// BROOOM_HOME that is not absolute makes the store unreachable.
	t.Setenv(config.HomeEnv, "relative/home")
	if lines, _ := complete(t, "undo", ""); len(lines) != 0 {
		t.Errorf("invalid home offered: %q", lines)
	}
}

// TestCompletePathArguments: the path argument and --path let the shell
// complete directories; the first argument of sweep is a preset.
func TestCompletePathArguments(t *testing.T) {
	emptyHome(t)
	dirs := fmt.Sprintf(":%d", cobra.ShellCompDirectiveFilterDirs)
	for _, args := range [][]string{{"review", ""}, {"sweep", "tidy", ""}, {"undo", "--path", ""}} {
		if _, directive := complete(t, args...); directive != dirs {
			t.Errorf("%q: directive %q, want %q", args, directive, dirs)
		}
	}
	for _, args := range [][]string{{"review", "a", ""}, {"sweep", "tidy", "a", ""}} {
		if lines, directive := complete(t, args...); len(lines) != 0 || directive != noFileComp {
			t.Errorf("%q: %q %q, want nothing", args, lines, directive)
		}
	}
}

func TestCompletionNeverWrites(t *testing.T) {
	home := emptyHome(t)
	complete(t, "--detector", "")
	complete(t, "undo", "")
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("completion created files in the home: %v", entries)
	}
}

func TestCompletionCommand(t *testing.T) {
	emptyHome(t)
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			code, out, errOut := run(t, "completion", shell)
			if code != ExitOK || !strings.Contains(out, "brooom") || errOut != "" {
				t.Fatalf("completion %s: code %d, stderr %q, %d bytes", shell, code, errOut, len(out))
			}
		})
	}

	code, out, _ := run(t, "completion", "--help")
	if code != ExitOK {
		t.Fatalf("completion --help: %d", code)
	}
	for _, want := range []string{
		"source <(brooom completion bash)",
		"/etc/bash_completion.d",
		"brew --prefix",
		"fpath",
		"compinit",
		"~/.config/fish/completions/brooom.fish",
		"brooom completion powershell | Out-String | Invoke-Expression",
		"$PROFILE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("completion help lacks %q", want)
		}
	}

	if _, out, _ := run(t, "--help"); !strings.Contains(out, "completion") {
		t.Errorf("root help does not list the completion command")
	}
}
