package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// sessionsHome points BROOOM_HOME at a temp dir and returns a store on it.
func sessionsHome(t *testing.T) *session.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.HomeEnv, home)
	return session.NewStore(filepath.Join(home, "sessions"))
}

func saveSession(t *testing.T, s *session.Store, id string, started time.Time, finished bool) {
	t.Helper()
	m := &session.Manifest{ID: id, StartedAt: started, Command: "sweep --apply"}
	m.Add(session.Entry{Action: "trash", Path: "/x/node_modules", SizeBytes: 3 << 20, Status: session.StatusApplied,
		Restorable: true, RecoveryHint: "restore from trash",
		Trash: &trash.Record{Strategy: config.StrategyTrash, StoredPath: "/trash/files/node_modules"}})
	m.Add(session.Entry{Action: "delete-branch", Path: "/x", Status: session.StatusFailed, Error: "boom"})
	if finished {
		m.Finish(started.Add(time.Second))
	}
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsEmpty(t *testing.T) {
	sessionsHome(t)
	code, out, _ := run(t, "sessions")
	if code != ExitOK || strings.TrimSpace(out) != "No sessions yet." {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = run(t, "sessions", "--format", "json")
	if code != ExitOK || !strings.Contains(out, `"sessions": []`) || !strings.Contains(out, `"problems": []`) {
		t.Fatalf("json code=%d out=%q", code, out)
	}
}

func TestSessionsTable(t *testing.T) {
	s := sessionsHome(t)
	now := time.Now()
	saveSession(t, s, "20260101-000000-aaaa", now.Add(-3*time.Hour), true)
	saveSession(t, s, "20260101-000001-bbbb", now.Add(-time.Hour), false)
	if err := os.WriteFile(filepath.Join(s.Dir, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"", "table"} {
		args := []string{"sessions"}
		if format != "" {
			args = append(args, "--format", format)
		}
		code, out, errOut := run(t, args...)
		if code != ExitOK {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		for _, want := range []string{"ID", "RECLAIMED", "20260101-000000-aaaa", "3h ago", "3.1 MB", "(unfinished)"} {
			if !strings.Contains(out, want) {
				t.Errorf("table lacks %q:\n%s", want, out)
			}
		}
		if strings.Index(out, "bbbb") > strings.Index(out, "aaaa") {
			t.Errorf("not newest first:\n%s", out)
		}
		if strings.Count(strings.TrimSpace(errOut), "\n") != 0 || !strings.Contains(errOut, "bad.json") {
			t.Errorf("stderr warning: %q", errOut)
		}
	}
}

func TestSessionsJSON(t *testing.T) {
	s := sessionsHome(t)
	saveSession(t, s, "20260101-000000-aaaa", time.Now(), true)
	if err := os.WriteFile(filepath.Join(s.Dir, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "sessions", "--format", "json")
	if code != ExitOK || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var got struct {
		Sessions []session.Manifest  `json:"sessions"`
		Problems []map[string]string `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(got.Sessions) != 1 || len(got.Problems) != 1 || got.Problems[0]["file"] == "" || got.Problems[0]["error"] == "" {
		t.Fatalf("got %+v", got)
	}
}

func TestSessionsDetail(t *testing.T) {
	s := sessionsHome(t)
	saveSession(t, s, "20260101-000000-aaaa", time.Now(), true)
	code, out, _ := run(t, "sessions", "20260101")
	if code != ExitOK {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"20260101-000000-aaaa", "Finished:", "sweep --apply", "[applied]", "/x/node_modules",
		"3.1 MB", "boom", "restore from trash", "strategy=trash", "/trash/files/node_modules", "restorable: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = run(t, "sessions", "20260101-000000-aaaa", "--format", "json")
	var m session.Manifest
	if code != ExitOK || json.Unmarshal([]byte(out), &m) != nil || len(m.Entries) != 2 {
		t.Fatalf("json detail code=%d out=%s", code, out)
	}
}

func TestSessionsErrors(t *testing.T) {
	s := sessionsHome(t)
	saveSession(t, s, "20260101-000000-aaaa", time.Now(), true)
	saveSession(t, s, "20260101-000000-bbbb", time.Now(), true)
	tests := []struct {
		name     string
		args     []string
		code     int
		contains string
	}{
		{"bad format", []string{"sessions", "--format", "xml"}, ExitUsage, "table, plain, json, ndjson"},
		{"bad format detail", []string{"sessions", "abc", "--format", "summary"}, ExitUsage, "table, plain, json, ndjson"},
		{"unknown id", []string{"sessions", "nope"}, ExitError, "nope"},
		{"ambiguous", []string{"sessions", "20260101"}, ExitError, "20260101-000000-bbbb"},
		{"traversal", []string{"sessions", "../x"}, ExitError, "invalid session id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := run(t, tt.args...)
			if code != tt.code || !strings.Contains(errOut, tt.contains) {
				t.Fatalf("code=%d stderr=%q", code, errOut)
			}
		})
	}
}
