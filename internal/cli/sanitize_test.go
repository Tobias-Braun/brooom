package cli

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
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
	code, _, errOut := run(t, "sessions", "no\x1b[31m\nFORGED")
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
	m := &session.Manifest{ID: "20260101-000000-aaaa", StartedAt: time.Now(), Command: "sweep\x1b[2J\nFORGED"}
	m.Add(session.Entry{Action: "trash", Path: "/x/dir\x1b]0;pwn\x07/na\nme", Status: session.StatusApplied,
		Error: "e\nFORGED", RecoveryHint: "h\nFORGED",
		Trash: &trash.Record{Strategy: config.StrategyTrash, StoredPath: "/t/a\x1bb\nFORGED"}})
	m.Finish(time.Now())
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"sessions"}, {"sessions", m.ID}} {
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

	t.Run("roots and config", func(t *testing.T) {
		cfg, work, _ := rootsEnv(t)
		hostileRoot := filepath.Join(work, hostile)
		body, _ := json.Marshal(map[string]any{"version": 1, "roots": []map[string]string{{"path": hostileRoot}}})
		writeFile(t, cfg, string(body))
		for _, args := range [][]string{{"roots", "list"}, {"roots", "list", "-f", "plain"}, {"config", "path"}, {"config", "show", "-f", "table"}} {
			code, out, errOut := run(t, args...)
			if code != ExitOK {
				t.Fatalf("%v: code %d, stderr %q", args, code, errOut)
			}
			requireSanitized(t, strings.Join(args, " "), out+errOut)
		}

		// Adding, re-adding and removing echo the path back.
		dir := mkdir(t, work, hostile+"2")
		for _, args := range [][]string{{"roots", "add", dir}, {"roots", "add", dir}, {"roots", "remove", dir}, {"roots", "remove", "no" + hostile}} {
			_, out, errOut := run(t, args...)
			requireSanitized(t, strings.Join(args, " "), out+errOut)
		}

		// The home warning names the directory.
		homeDir := mkdir(t, work, "home"+hostile)
		t.Setenv("HOME", homeDir)
		t.Setenv("USERPROFILE", homeDir)
		_, out, errOut := run(t, "roots", "add", homeDir)
		requireSanitized(t, "roots add home", out+errOut)

		// Validation problems name the config path, the field and the message.
		writeFile(t, cfg, `{"version":1,"trash":{"strategy":"sh`+`\u001b[31m\nFORGED"},"roots":[{"path":"rel\u001b\nFORGED"}]}`)
		code, out, errOut := run(t, "config", "validate")
		if code == ExitOK {
			t.Fatal("want validation problems")
		}
		requireSanitized(t, "config validate", out+errOut)
		code, out, errOut = run(t, "config", "validate", "--config", filepath.Join(work, hostile+".json"))
		requireSanitized(t, "config validate missing file", out+errOut)
		_ = code
	})

	t.Run("undo and purge", func(t *testing.T) {
		f := agedFixture(t)
		id := "20260701-100000-\x1b[2J\nFORGED"
		f.session(id, purgeClock.Add(-60*24*time.Hour), f.write(hostile+"/f.txt", "x"))
		ageQuarantine(t, f, id, purgeClock.Add(-60*24*time.Hour))

		for _, args := range [][]string{{"undo", id}, {"undo", id, "--yes"}, {"undo", id}, {"purge"}, {"purge", "--yes"}} {
			_, out, errOut := runApp(t, "", false, purgeClock, args...)
			requireSanitized(t, strings.Join(args, " "), out+errOut)
		}
	})

	t.Run("clean", func(t *testing.T) {
		f := newUndoFixture(t)
		// The trashed directory has a plain name: a step's shell command is
		// printed verbatim on purpose so it stays copy-pasteable (its quoting
		// is tracked in #129), so a hostile name there would trip the check.
		// The refused finding, its id and the session id are covered.
		dir, _ := junkDir(t, f.repo.Dir, "junk")
		outside := t.TempDir()
		bad := trashFinding(outside, filepath.Join(outside, hostile))
		bad.ID = "id" + hostile
		report := writeReportFile(t, trashFinding(f.repo.Dir, dir), bad)
		for _, args := range [][]string{
			{"clean", "--from", report},
			{"clean", "--from", report, "--yes"},
		} {
			_, out, errOut := runApp(t, "", false, time.Time{}, append(args, quarantine...)...)
			requireSanitized(t, strings.Join(args, " "), out+errOut)
		}
	})
}
