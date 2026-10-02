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
	for _, cmd := range [][]string{{"config", "show"}, {"sweep", "--dry-run"}} {
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
	code, out, errOut := run(t, "config", "show")
	if code != ExitOK || !strings.Contains(out, `"version": 1`) {
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
}

func TestSessionsSummaryStaysUnsupported(t *testing.T) {
	twoSessions(t)
	if code, _, _ := run(t, "sessions", "-f", "summary"); code != ExitUsage {
		t.Errorf("code = %d, want usage error", code)
	}
}

func TestScanOnlyFlagsRejectedElsewhere(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{"config show -d", []string{"config", "show", "-d", "nope"}, false},
		{"config path -f", []string{"config", "path", "-f", "json"}, false},
		{"empty-trash -d", []string{"empty-trash", "-d", "x"}, false},
		{"empty-trash -f", []string{"empty-trash", "-f", "json"}, false},
		{"version -d", []string{"version", "-d", "x"}, false},
		{"sessions -d", []string{"sessions", "-d", "x"}, false},
		{"config show -f", []string{"config", "show", "-f", "json"}, true},
		{"sessions -f", []string{"sessions", "-f", "json"}, true},
		{"version -f", []string{"version", "-f", "json"}, true},
		{"config path plain", []string{"config", "path"}, true},
		{"sweep -d rejects unknown detector only", []string{"sweep", "-d", "nope", "--dry-run"}, false},
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
	for path, flags := range map[string][]string{
		"brooom sweep":  {"detector", "format"},
		"brooom review": {"detector"},
	} {
		for _, f := range flags {
			if msg := unsupportedScanFlag(path, f); msg != "" {
				t.Errorf("%s must accept --%s", path, f)
			}
		}
	}
	if unsupportedScanFlag("brooom review", "format") == "" {
		t.Error("review prints text only and must reject --format")
	}
}
