package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// visibleCommands returns every non-hidden command of a fresh tree,
// including the root.
func visibleCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	walkCommands(root, func(c *cobra.Command) { out = append(out, c) })
	return out
}

func TestEveryCommandHasShortAndExample(t *testing.T) {
	for _, c := range visibleCommands(NewRootCommand()) {
		path := c.CommandPath()
		if c.Short == "" {
			t.Errorf("%s: empty Short", path)
		}
		if strings.HasSuffix(c.Short, ".") {
			t.Errorf("%s: Short %q ends with a period", path, c.Short)
		}
		if strings.TrimSpace(c.Example) == "" {
			t.Errorf("%s: empty Example", path)
		}
	}
}

func TestExampleCountPerCommand(t *testing.T) {
	for _, c := range visibleCommands(NewRootCommand()) {
		n := len(exampleCommands(c.Example))
		// The completion subcommands' examples are shell snippets with pipes
		// and redirects; 1 to 4 brooom invocations is the rule everywhere.
		if n < 1 || n > 5 {
			t.Errorf("%s: %d example invocations, want 1 to 4 (pipelines may add one)", c.CommandPath(), n)
		}
	}
}

// exampleInvocation matches a `brooom ...` invocation inside an example line,
// ending at a shell metacharacter (pipe, redirect, closing parenthesis).
var exampleInvocation = regexp.MustCompile(`\bbrooom\b[^|><)&;]*`)

// exampleCommands extracts the brooom invocations of an Example block.
func exampleCommands(example string) []string {
	var out []string
	for _, line := range strings.Split(example, "\n") {
		for _, m := range exampleInvocation.FindAllString(line, -1) {
			out = append(out, strings.TrimSpace(m))
		}
	}
	return out
}

// TestExamplesParse resolves every example against the real command tree and
// parses its flags, so an example can never mention a flag or command that
// does not exist.
func TestExamplesParse(t *testing.T) {
	for _, c := range visibleCommands(NewRootCommand()) {
		for _, ex := range exampleCommands(c.Example) {
			t.Run(ex, func(t *testing.T) {
				args := strings.Fields(strings.ReplaceAll(ex, `"`, ""))[1:]
				target, rest, err := NewRootCommand().Find(args)
				if err != nil {
					t.Fatalf("%s: %v", ex, err)
				}
				if err := target.ParseFlags(rest); err != nil {
					t.Fatalf("%s: %v", ex, err)
				}
				if err := target.ValidateArgs(target.Flags().Args()); err != nil {
					t.Fatalf("%s: %v", ex, err)
				}
			})
		}
	}
}

func TestExampleExtraction(t *testing.T) {
	got := exampleCommands("  brooom scan -f json > out.json\n  brooom scan | brooom clean --from -\n  source <(brooom completion bash)")
	want := []string{"brooom scan -f json", "brooom scan", "brooom clean --from -", "brooom completion bash"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestExamplesRejectUnknowns proves the example check has teeth: an unknown
// flag or command must fail the same resolution the real test uses.
func TestExamplesRejectUnknowns(t *testing.T) {
	for _, ex := range []string{"brooom scan --no-such-flag", "brooom nosuchcommand", "brooom scan extra"} {
		args := strings.Fields(ex)[1:]
		target, rest, err := NewRootCommand().Find(args)
		if err == nil {
			err = target.ParseFlags(rest)
		}
		if err == nil {
			err = target.ValidateArgs(target.Flags().Args())
		}
		if err == nil {
			t.Errorf("%q was accepted", ex)
		}
	}
}

// TestNewRootCommandHasNoSideEffects guards the promise that the generator
// can build the tree without touching the user's home.
func TestNewRootCommandHasNoSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BROOOM_HOME", filepath.Join(home, "brooom"))
	t.Setenv("HOME", home)
	NewRootCommand()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("constructing the tree created %v", entries)
	}
}

func normalizeNewlines(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// TestCLIReferenceUpToDate regenerates docs/cli.md in memory and compares it
// with the committed file, so the reference cannot drift from the commands.
func TestCLIReferenceUpToDate(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("..", "..", "docs", "cli.md"))
	if err != nil {
		t.Fatalf("docs/cli.md is missing: %v\nrun: go run ./internal/tools/gendocs", err)
	}
	want := ReferenceMarkdown(NewRootCommand())
	if normalizeNewlines(string(committed)) != want {
		t.Fatal("docs/cli.md is out of date; run: go run ./internal/tools/gendocs")
	}
}

func TestReferenceIsDeterministicAndPortable(t *testing.T) {
	doc := ReferenceMarkdown(NewRootCommand())
	if doc != ReferenceMarkdown(NewRootCommand()) {
		t.Error("two renderings differ")
	}
	if strings.Contains(doc, "\r") {
		t.Error("document contains CR")
	}
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	for _, p := range []string{cwd, home} {
		if p != "" && p != "/" && strings.Contains(doc, p) {
			t.Errorf("document contains machine-specific path %q", p)
		}
	}
	for _, want := range []string{"## Global flags", "## `brooom sweep`", "## `brooom config init`", "## `brooom completion bash`", "**Examples**"} {
		if !strings.Contains(doc, want) {
			t.Errorf("document lacks %q", want)
		}
	}
}

func TestSymbolicDefaults(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	if got := symbolic(filepath.Join(home, ".brooom", "config.json")); strings.Contains(got, home) || !strings.HasPrefix(got, "~") {
		t.Errorf("symbolic = %q", got)
	}
}
