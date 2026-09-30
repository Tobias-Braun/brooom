package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// missingConfigArgs points --config at a file that does not exist.
func missingConfigArgs(t *testing.T) []string {
	t.Helper()
	t.Setenv(config.HomeEnv, t.TempDir())
	return []string{"--config", filepath.Join(t.TempDir(), "typo.json")}
}

func TestExplicitMissingConfigIsAnError(t *testing.T) {
	for _, cmd := range [][]string{{"config", "validate"}, {"config", "show"}, {"roots", "list"}} {
		t.Run(strings.Join(cmd, " "), func(t *testing.T) {
			args := append(missingConfigArgs(t), cmd...)
			code, out, errOut := run(t, args...)
			if code != ExitError || !strings.Contains(errOut, "config file not found") {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
		})
	}
}

func TestImplicitMissingConfigKeepsDefaults(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	code, out, errOut := run(t, "config", "validate")
	if code != ExitOK || !strings.Contains(out, "defaults apply") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestExplicitMissingConfigStillAllowsInit(t *testing.T) {
	args := append(missingConfigArgs(t), "config", "init")
	if code, _, errOut := run(t, args...); code != ExitOK {
		t.Fatalf("config init must create the explicit file: %d %q", code, errOut)
	}
}

// twoSessions saves two manifests and returns nothing; the newest is bbbb.
func twoSessions(t *testing.T) {
	t.Helper()
	s := sessionsHome(t)
	saveSession(t, s, "20260101-000000-aaaa", time.Now().Add(-time.Hour), true)
	saveSession(t, s, "20260102-000000-bbbb", time.Now(), true)
}

func TestSessionsPlain(t *testing.T) {
	twoSessions(t)
	code, out, errOut := run(t, "sessions", "-f", "plain")
	if code != ExitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 2 || lines[0] != "20260102-000000-bbbb" {
		t.Errorf("plain list = %q", out)
	}
	code, out, _ = run(t, "sessions", "20260101", "-f", "plain")
	if code != ExitOK || !strings.Contains(out, "/x/node_modules") {
		t.Errorf("plain detail code=%d out=%q", code, out)
	}
}

func TestSessionsNDJSON(t *testing.T) {
	twoSessions(t)
	code, out, errOut := run(t, "sessions", "-f", "ndjson")
	if code != ExitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson lines = %q", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil || m["id"] == nil {
		t.Errorf("ndjson line %q: %v", lines[0], err)
	}
	code, out, _ = run(t, "sessions", "20260101", "-f", "ndjson")
	if code != ExitOK || len(strings.Split(strings.TrimSpace(out), "\n")) != 2 {
		t.Errorf("ndjson detail code=%d out=%q", code, out)
	}
}

func TestSessionsSummaryStaysUnsupported(t *testing.T) {
	twoSessions(t)
	if code, _, _ := run(t, "sessions", "-f", "summary"); code != ExitUsage {
		t.Errorf("code = %d, want usage error", code)
	}
}

func TestRootsListNDJSON(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	dir := t.TempDir()
	if code, _, errOut := run(t, "roots", "add", dir); code != ExitOK {
		t.Fatal(errOut)
	}
	code, out, errOut := run(t, "roots", "list", "-f", "ndjson")
	if code != ExitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &info); err != nil || info["path"] == nil {
		t.Errorf("out %q: %v", out, err)
	}
}

func TestScanOnlyFlagsRejectedElsewhere(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{"validate -d", []string{"config", "validate", "-d", "nope"}, false},
		{"validate -w", []string{"config", "validate", "-w"}, false},
		{"validate --root", []string{"config", "validate", "--root", "x"}, false},
		{"validate -f", []string{"config", "validate", "-f", "json"}, false},
		{"roots add -d", []string{"roots", "add", "-d", "x", "."}, false},
		{"purge -d", []string{"purge", "-d", "x"}, false},
		{"version -w", []string{"version", "-w"}, false},
		{"sessions -d", []string{"sessions", "-d", "x"}, false},
		{"config show -w", []string{"config", "show", "-w"}, false},
		{"config show -f", []string{"config", "show", "-f", "json"}, true},
		{"sessions -f", []string{"sessions", "-f", "json"}, true},
		{"version -f", []string{"version", "-f", "json"}, true},
		{"config validate plain", []string{"config", "validate"}, true},
		{"scan -d rejects unknown detector only", []string{"scan", "-d", "nope"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := run(t, tt.args...)
			if tt.ok && code != ExitOK {
				t.Fatalf("code=%d err=%q", code, errOut)
			}
			if !tt.ok && code != ExitUsage {
				t.Fatalf("code=%d err=%q, want usage error", code, errOut)
			}
		})
	}
}

func TestScanFlagsStillAcceptedWhereUsed(t *testing.T) {
	root := newRootCmd(&app{})
	for _, path := range [][]string{{"scan"}, {"sweep"}, {"branches"}, {"worktrees"}, {"logs"}, {"artifacts"}, {"ai"}, {"clean"}, {"git", "purge"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"workspaces", "root", "detector", "format"} {
			if msg := unsupportedScanFlag(cmd.CommandPath(), f); msg != "" {
				t.Errorf("%s must accept --%s", cmd.CommandPath(), f)
			}
		}
	}
}

func TestUpdateNoticeFollowsConfigFormat(t *testing.T) {
	f := newReleaseFixture(t, 200, "v2.0.0", 0)
	a, _, errOut := newTestApp(t, "1.0.0")
	a.update.stdoutTTY = func() bool { return true }
	a.update.loadConfig = func(string) (*config.Config, error) {
		return &config.Config{UpdateCheck: true, Output: config.Output{Format: "json"}}, nil
	}
	a.update.grace = 200 * time.Millisecond
	if code := execute(a, []string{"version"}); code != ExitOK {
		t.Fatal(errOut.String())
	}
	if strings.Contains(errOut.String(), "available") || f.hits.Load() != 0 {
		t.Errorf("notice must be suppressed for output.format json: stderr %q hits %d", errOut, f.hits.Load())
	}
}

func TestUpdateCheckJSONVersionsWithoutPrefix(t *testing.T) {
	newReleaseFixture(t, 200, "v1.4.0", 0)
	a, out, errOut := newTestApp(t, "v1.2.0")
	if code := execute(a, []string{"update-check", "-f", "json"}); code != ExitOK {
		t.Fatal(errOut.String())
	}
	var got updateReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Current != "1.2.0" || got.Latest != "1.4.0" {
		t.Errorf("current %q latest %q, want bare versions", got.Current, got.Latest)
	}
}
