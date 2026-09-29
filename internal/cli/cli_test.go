package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// run executes the CLI in-process and returns exit code, stdout and stderr.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main(args, IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	return code, out.String(), errOut.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := run(t, "version")
	if code != ExitOK || !strings.HasPrefix(out, "brooom ") {
		t.Fatalf("version: code=%d out=%q", code, out)
	}
	code, out, _ = run(t, "version", "--format", "json")
	if code != ExitOK {
		t.Fatalf("version json: code=%d", code)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(out), &v); err != nil || v["version"] == "" {
		t.Fatalf("version json output %q: %v", out, err)
	}
}

func TestUsageErrorExitCode(t *testing.T) {
	code, _, errOut := run(t, "--definitely-not-a-flag")
	if code != ExitUsage {
		t.Fatalf("code = %d, want %d (stderr %q)", code, ExitUsage, errOut)
	}
}

func TestHelpListsCommands(t *testing.T) {
	code, out, _ := run(t, "--help")
	if code != ExitOK {
		t.Fatalf("help exit code %d", code)
	}
	for _, c := range []string{"scan", "sweep", "branches", "worktrees", "undo", "roots", "config"} {
		if !strings.Contains(out, c) {
			t.Errorf("help does not mention %q", c)
		}
	}
}
