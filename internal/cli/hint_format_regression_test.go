package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// TestFileWorkflowWorksWithAConfirmationPrompt reproduces #214: `scan |
// clean --from -` leaves stdin as the pipe, so a findings file is the way to
// act on a reviewed selection, and it must work interactively.
func TestFileWorkflowWorksWithAConfirmationPrompt(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	_, out, _ := brooom(t, "", "scan", "-d", "merged-branch,"+config.DetectorLogs, "--format", "json")
	file := writeRaw(t, out)
	code, _, errOut := runApp(t, "y\n", true, time.Time{}, "clean", "--from", file, "--trash-strategy", "quarantine")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if f.hasBranch("feat/merged") {
		t.Errorf("clean did not act: %v", f.branches())
	}
}

// TestCleanRejectsExplicitFormat reproduces #216: clean printed human text
// for -f json with exit 0.
func TestCleanRejectsExplicitFormat(t *testing.T) {
	f := newCleanupFixture(t, nil)
	dir, _ := junkDir(t, f.repo.Dir, "target")
	report := writeReportFile(t, trashFinding(f.repo.Dir, dir))
	for _, format := range []string{"json", "ndjson", "plain", "tree", "summary", "table"} {
		t.Run(format, func(t *testing.T) {
			code, _, errOut := clean(t, "", "--from", report, "-f", format, "--yes")
			if code != ExitUsage || !strings.Contains(errOut, "--format") {
				t.Errorf("code %d, stderr %q", code, errOut)
			}
			if !exists(dir) {
				t.Error("a rejected clean removed something")
			}
		})
	}
}

func TestCleanIgnoresConfigFormat(t *testing.T) {
	f := newCleanupFixture(t, map[string]any{"output": map[string]any{"format": "ndjson"}})
	dir, _ := junkDir(t, f.repo.Dir, "target")
	report := writeReportFile(t, trashFinding(f.repo.Dir, dir))
	if code, _, errOut := clean(t, "", "--from", report, "--dry-run"); code != ExitOK {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestGitPurgeRejectsNonTableFormatWithOperations(t *testing.T) {
	newPurgeFixture(t, nil)
	for _, format := range []string{"json", "ndjson", "plain", "summary", "tree"} {
		t.Run(format, func(t *testing.T) {
			code, _, errOut := brooom(t, "", "git", "purge", "--prune", "now", "--format", format, "--dry-run")
			if code != ExitUsage || !strings.Contains(errOut, "--format "+format) {
				t.Errorf("code %d, stderr %q", code, errOut)
			}
		})
	}
	if code, _, errOut := brooom(t, "", "git", "purge", "--prune", "now", "--format", "table", "--dry-run"); code != ExitOK {
		t.Errorf("explicit table: code %d, stderr %q", code, errOut)
	}
}

func TestGitPurgeIgnoresConfigTreeFormat(t *testing.T) {
	newPurgeFixture(t, map[string]any{"output": map[string]any{"format": "tree"}})
	if code, _, errOut := brooom(t, "", "git", "purge", "--prune", "now", "--dry-run"); code != ExitOK {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

// TestNoticeFollowsTheRenderedFormat reproduces #237: `sessions` ignores
// output.format, so a configured machine format must not hide the notice; a
// findings command that does honour it still suppresses it.
func TestNoticeFollowsTheRenderedFormat(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"sessions ignores config format", []string{"sessions"}, true},
		// sessionsFormat in sessions_render.go accepts -f json; that explicit
		// machine format suppresses the notice, unlike the configured
		// output.format above, which sessions never reads.
		{"sessions with explicit json", []string{"sessions", "-f", "json"}, false},
		{"clean ignores config format", []string{"clean", "--from", "EMPTY"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := agedFixture(t)
			writeConfig(t, f.home, map[string]any{"output": map[string]any{"format": "ndjson"}})
			args := slices.Clone(tt.args)
			for i, a := range args {
				if a == "EMPTY" {
					args[i] = writeReportFile(t)
				}
			}
			code, _, errOut := runApp(t, "", false, purgeClock, args...)
			if code != ExitOK {
				t.Fatalf("code %d, stderr %q", code, errOut)
			}
			if got := strings.Contains(errOut, "retention, run 'brooom purge'"); got != tt.want {
				t.Errorf("notice printed = %v, want %v; stderr %q", got, tt.want, errOut)
			}
		})
	}
}

func TestUpdateCheckFormatFollowsTheRenderedFormat(t *testing.T) {
	a := &app{}
	cmd := leafFor(t, a, []string{"sessions"})
	if got, _ := a.renderedFormat(cmd, "ndjson"); !isTableFormat(got) {
		t.Errorf("sessions renders %q, want table", got)
	}
	scan := leafFor(t, a, []string{"scan"})
	if got, _ := a.renderedFormat(scan, "ndjson"); got != "ndjson" {
		t.Errorf("scan renders %q, want ndjson", got)
	}
	acting := leafFor(t, a, []string{"sweep"})
	if got, _ := a.renderedFormat(acting, "ndjson"); !isTableFormat(got) {
		t.Errorf("an acting sweep renders %q, want table", got)
	}
	dry := &app{}
	if got, _ := dry.renderedFormat(leafFor(t, dry, []string{"sweep", "--dry-run"}), "ndjson"); got != "ndjson" {
		t.Errorf("a dry-run sweep renders %q, want the configured ndjson", got)
	}
	explicit := &app{}
	if got, _ := explicit.renderedFormat(leafFor(t, explicit, []string{"sweep", "-f", "json"}), ""); got != "json" {
		t.Errorf("explicit format lost: %q", got)
	}
}
