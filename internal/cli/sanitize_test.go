package cli

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// requireNoControl fails on any control character except newline.
func requireNoControl(t *testing.T, what, s string) {
	t.Helper()
	for _, r := range strings.ReplaceAll(s, "\n", "") {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("%s holds control rune %q:\n%q", what, r, s)
		}
	}
}

// TestErrorOutputSanitized covers CLI error printing: the session id from the
// command line ends up in the "not found" error.
func TestErrorOutputSanitized(t *testing.T) {
	sessionsHome(t)
	code, _, errOut := run(t, "undo", "no\x1b[31m\nFORGED")
	if code == ExitOK {
		t.Fatal("want an error for an unknown session")
	}
	requireNoControl(t, "stderr", errOut)
	if strings.Contains(errOut, "\nFORGED") {
		t.Errorf("forged line in stderr:\n%q", errOut)
	}
}

func TestSessionsRenderSanitized(t *testing.T) {
	s := sessionsHome(t)
	m := &session.Manifest{ID: "20260101-000000-aaaa", StartedAt: time.Now(), Command: "sweep", Root: "/x/repo\x1b]0;pwn\x07\nFORGED"}
	m.Add(session.Entry{Action: "trash", Path: "/x/dir", Status: session.StatusApplied,
		Trash: &trash.Record{Strategy: trash.StrategyTrash, StoredPath: "/t/a"}})
	m.Finish(time.Now())
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"sessions"}, {"sessions", "-f", "plain"}} {
		code, out, _ := run(t, args...)
		if code != ExitOK {
			t.Fatalf("%v: code %d", args, code)
		}
		requireNoControl(t, strings.Join(args, " "), out)
		if strings.Contains(out, "\nFORGED") {
			t.Errorf("%v: forged line:\n%q", args, out)
		}
	}
}

// requireSanitized fails when a command's output holds a control rune (other
// than the newline that ends a line) or a line forged through an injected
// newline. Every hostile value in the tests below starts its forged line with
// FORGED, so a raw newline in front of it shows up as a line of its own.
func requireSanitized(t *testing.T, what, s string) {
	t.Helper()
	requireNoControl(t, what, s)
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "FORGED") {
			t.Errorf("%s holds a forged line %q:\n%s", what, l, s)
		}
	}
}

// hostile is a name with a terminal escape and a newline, valid as a file or
// directory name on Unix.
const hostile = "a\x1b[31m\nFORGED"

// TestEveryHumanOutputSanitized renders each command that prints paths, ids
// or messages with hostile values and checks stdout and stderr, so a new
// unsanitised print in one of these commands cannot slip in unnoticed.
func TestEveryHumanOutputSanitized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("control characters are not valid in Windows file names")
	}

	t.Run("config and path", func(t *testing.T) {
		cfg, work, _ := configEnv(t)
		for _, args := range [][]string{{"config", "path"}, {"config", "show", "-f", "table"}} {
			code, out, errOut := run(t, args...)
			if code != ExitOK {
				t.Fatalf("%v: code %d, stderr %q", args, code, errOut)
			}
			requireSanitized(t, strings.Join(args, " "), out+errOut)
		}

		// A path argument that does not exist is echoed back in the error.
		_, pathOut, pathErr := run(t, "sweep", "tidy", filepath.Join(work, hostile))
		requireSanitized(t, "sweep hostile path", pathOut+pathErr)

		// Validation problems name the config path, the field and the message.
		writeFile(t, cfg, `{"version":1,"output":{"format":"x\u001b\nFORGED"},"scan":{"skip_dirs":["a/\u001b\nFORGED"]}}`)
		code, out, errOut := run(t, "config", "show")
		if code == ExitOK {
			t.Fatal("want validation problems")
		}
		requireSanitized(t, "config show", out+errOut)
		_, out, errOut = run(t, "config", "show", "--config", filepath.Join(work, hostile+".json"))
		requireSanitized(t, "config show missing file", out+errOut)
	})

	t.Run("undo", func(t *testing.T) {
		f := newUndoFixture(t)
		id := "20260701-100000-\x1b[2J\nFORGED"
		f.session(id, time.Now(), f.write(hostile+"/f.txt", "x"))
		for _, args := range [][]string{{"undo", id}, {"undo", id, "--yes"}, {"undo", id}, {"sessions"}} {
			_, out, errOut := runApp(t, "", false, time.Time{}, args...)
			requireSanitized(t, strings.Join(args, " "), out+errOut)
		}
	})
}
