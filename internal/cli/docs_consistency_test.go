package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestGlobalFlagsSentenceMatchesBehaviour reproduces #239: the generated
// reference claimed the global flags are accepted by every command, while
// `version -w` and `undo -f json` exit 2.
func TestGlobalFlagsSentenceMatchesBehaviour(t *testing.T) {
	doc := ReferenceMarkdown(NewRootCommand())
	if strings.Contains(doc, "accepted by every command") {
		t.Error("cli.md claims every command accepts the global flags")
	}
	if !strings.Contains(doc, "exit code 2") {
		t.Error("cli.md does not say that ignored flags are rejected with exit 2")
	}
	isolate(t)
	for _, args := range [][]string{{"version", "-w"}, {"undo", "-f", "json"}, {"purge", "-f", "json"}} {
		if code, _, _ := brooom(t, "", args...); code != ExitUsage {
			t.Errorf("%v exit %d, want %d", args, code, ExitUsage)
		}
	}
}

// TestCompletionBashHelpNamesSudo reproduces #239: cobra's default help of
// `completion bash` redirected into /etc/bash_completion.d without root.
func TestCompletionBashHelpNamesSudo(t *testing.T) {
	isolate(t)
	code, out, errOut := brooom(t, "", "completion", "bash", "--help")
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"sudo tee /etc/bash_completion.d/brooom", "~/.local/share/bash-completion/completions"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "> /etc/bash_completion.d/brooom") {
		t.Errorf("help still redirects into /etc without root:\n%s", out)
	}
}

// TestArchitectureNamesExistingTrashFiles reproduces #239: ARCHITECTURE.md
// named trash_*.go files that do not exist.
func TestArchitectureNamesExistingTrashFiles(t *testing.T) {
	arch, err := os.ReadFile(filepath.Join("..", "..", "docs", "ARCHITECTURE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"`trash_windows.go`", "`trash_darwin.go`", "`trash_unix.go`"} {
		if strings.Contains(string(arch), name) {
			t.Errorf("ARCHITECTURE.md names %s, which does not exist", name)
		}
	}
	for _, name := range []string{"ostrash_windows.go", "ostrash_darwin.go", "mactrash.go", "ostrash_unix.go"} {
		if !strings.Contains(string(arch), name) {
			t.Errorf("ARCHITECTURE.md does not name %s", name)
		}
		if _, err := os.Stat(filepath.Join("..", "trash", name)); err != nil {
			t.Errorf("%s does not exist: %v", name, err)
		}
	}
}

// TestSpecListsFormatExceptions keeps SPEC.md honest about the commands that
// reject --format: every such command of the tree must be named there.
func TestSpecListsFormatExceptions(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "docs", "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.HasSubCommands() {
				walk(sub)
				continue
			}
			path := sub.CommandPath()
			if isCompletionOrHelp(sub) || !ignoresScanFlag(path, "format") {
				continue
			}
			if want := "`" + strings.TrimPrefix(path, "brooom ") + "`"; !strings.Contains(string(spec), want) {
				t.Errorf("SPEC.md does not mention %s as a --format exception", want)
			}
		}
	}
	walk(NewRootCommand())
}
