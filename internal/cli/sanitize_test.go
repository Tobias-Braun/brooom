package cli

import (
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
