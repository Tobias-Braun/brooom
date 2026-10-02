package cli

import (
	"runtime"
	"strings"
	"testing"
)

// assertLines fails unless every want fragment starts its own stderr line and
// no literal backslash-n survived, which is the symptom of the whole message
// being escaped as one line.
func assertLines(t *testing.T, errOut string, wants ...string) {
	t.Helper()
	if strings.Contains(errOut, `\n`) {
		t.Errorf("stderr holds an escaped newline instead of a line break:\n%s", errOut)
	}
	lines := strings.Split(errOut, "\n")
	for _, want := range wants {
		found := false
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no stderr line starts with %q:\n%s", want, errOut)
		}
	}
}

func TestMultiLineErrorsRenderPerLine(t *testing.T) {
	t.Run("sweep with an invalid config", func(t *testing.T) {
		cfg, _, _ := configEnv(t)
		writeFile(t, cfg, `{"version":1,"scan":{"max_depth":-1},"output":{"format":"xml"}}`)
		code, _, errOut := run(t, "sweep", "--dry-run")
		if code != ExitError {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		assertLines(t, errOut, "scan.max_depth", "output.format")
	})
}

// TestMultiLineErrorsCannotForgeLines pins the reason the message parts are
// sanitized when the error is built: a newline inside a user-controlled path
// must stay escaped and never start a line of its own.
func TestMultiLineErrorsCannotForgeLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("newlines are not valid in Windows file names")
	}
	const evil = "x\nforged: line"
	t.Run("path argument", func(t *testing.T) {
		configEnv(t)
		_, _, errOut := run(t, "sweep", "tidy", evil)
		assertNoForgedLine(t, errOut)
	})
	t.Run("invalid config", func(t *testing.T) {
		cfg, _, _ := configEnv(t)
		writeFile(t, cfg, `{"version":1,"output":{"format":"rel\nforged: yes"}}`)
		_, _, errOut := run(t, "sweep", "--dry-run")
		assertNoForgedLine(t, errOut)
		if !strings.Contains(errOut, "output.format") {
			t.Errorf("problem missing:\n%s", errOut)
		}
	})
}

func assertNoForgedLine(t *testing.T, errOut string) {
	t.Helper()
	for _, l := range strings.Split(errOut, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "forged") {
			t.Errorf("forged line in stderr:\n%s", errOut)
		}
	}
}
