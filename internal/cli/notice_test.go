package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

const noticeStart = "brooom: 1 quarantined session ("

func TestRetentionNoticeOnStderrOnly(t *testing.T) {
	agedFixture(t)
	// "sessions" is a cheap read-only command that is not exempt.
	code, out, errOut := runApp(t, "", false, purgeClock, "sessions")
	if code != ExitOK {
		t.Fatalf("code=%d", code)
	}
	if strings.Count(errOut, "quarantined") != 1 || !strings.Contains(errOut, noticeStart) ||
		!strings.Contains(errOut, "is past the 14-day retention, run 'brooom purge' to free the space") {
		t.Fatalf("stderr = %q", errOut)
	}
	if strings.Contains(out, "quarantined") {
		t.Fatalf("notice leaked to stdout: %q", out)
	}
}

func TestRetentionNoticeUsesConfiguredDaysAndPlural(t *testing.T) {
	f := agedFixture(t)
	writeConfig(t, f.home, map[string]any{"trash": map[string]any{"quarantine_retention_days": 1}})
	ageQuarantine(t, f, newSession, purgeClock.Add(-5*24*time.Hour))
	_, _, errOut := runApp(t, "", false, purgeClock, "sessions")
	if !strings.Contains(errOut, "brooom: 2 quarantined sessions (") || !strings.Contains(errOut, "are past the 1-day retention") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestRetentionNoticeSuppressed(t *testing.T) {
	tests := []struct {
		name string
		args []string
		cfg  map[string]any
	}{
		{"json", []string{"sessions", "--format", "json"}, nil},
		{"ndjson", []string{"sessions", "--format", "ndjson"}, nil},
		{"plain", []string{"sessions", "--format", "plain"}, nil},
		{"quiet", []string{"sessions", "--quiet"}, nil},
		{"purge", []string{"purge"}, nil},
		{"undo", []string{"undo"}, nil},
		{"version", []string{"version"}, nil},
		{"help", []string{"help"}, nil},
		{"retention 0", []string{"sessions"}, map[string]any{"trash": map[string]any{"quarantine_retention_days": 0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := agedFixture(t)
			if tt.cfg != nil {
				writeConfig(t, f.home, tt.cfg)
			}
			_, _, errOut := runApp(t, "", false, purgeClock, tt.args...)
			if strings.Contains(errOut, "past the") && strings.Contains(errOut, "retention, run") {
				t.Fatalf("notice printed: %q", errOut)
			}
		})
	}
}

func TestRetentionNoticeSilentWithoutExpiredOrOnErrors(t *testing.T) {
	newUndoFixture(t)
	_, _, errOut := runApp(t, "", false, purgeClock, "sessions")
	if errOut != "" {
		t.Fatalf("stderr = %q", errOut)
	}
	// A broken config is reported by the command, never by the notice.
	code, _, errOut := runApp(t, "", false, purgeClock, "scan", "--config", "/definitely/missing.json")
	if code == ExitOK || strings.Contains(errOut, "retention") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestRetentionNoticeNotPrintedAfterFailedCommand(t *testing.T) {
	agedFixture(t)
	code, _, errOut := runApp(t, "", false, purgeClock, "undo", "zzz")
	if code != ExitError || strings.Contains(errOut, "retention") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

// TestRetentionNoticeCoexistsWithOtherHooks checks the single root hook runs
// the notice and any later hook, in registration order.
func TestRetentionNoticeCoexistsWithOtherHooks(t *testing.T) {
	agedFixture(t)
	var out, errOut bytes.Buffer
	a := &app{io: IO{In: strings.NewReader(""), Out: &out, Err: &errOut}, clock: func() time.Time { return purgeClock }}
	root := newRootCmd(a)
	a.postRunHooks = append(a.postRunHooks, func(*cobra.Command, []string) { errOut.WriteString("custom hook ran\n") })
	root.SetArgs([]string{"sessions"})
	root.SetOut(&out)
	root.SetErr(&errOut)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	notice, custom := strings.Index(errOut.String(), "quarantined"), strings.Index(errOut.String(), "custom hook ran")
	if notice < 0 || custom < notice {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRetentionMessage(t *testing.T) {
	got := retentionMessage(3, 2_500_000, 7)
	want := "brooom: 3 quarantined sessions (2.5 MB) are past the 7-day retention, run 'brooom purge' to free the space"
	if got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}
