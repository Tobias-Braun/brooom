package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// hintFinding is an actionable finding of the given detector and confidence.
func hintFinding(detector string, c findings.Confidence, name string) findings.Finding {
	return findings.Finding{
		ID:              findings.NewID(detector, findings.KindDir, "/r/"+name, ""),
		Detector:        detector,
		Path:            "/r/" + name,
		Kind:            findings.KindDir,
		Confidence:      c,
		SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash},
	}
}

// hintFor renders the scan footer for args over the given findings.
func hintFor(t *testing.T, cfg *config.Config, args []string, fs ...findings.Finding) string {
	t.Helper()
	a := &app{args: args, goos: "linux"}
	res := &scanResult{Report: findings.NewReport("test", time.Time{}, nil, fs, nil), Config: cfg}
	return a.applyHint(leafFor(t, a, args), res)
}

// TestScanPipelineHintIsFileBased reproduces #214: `scan | clean --from -`
// leaves stdin as the pipe, so the confirmation is refused. The hint must
// name the two-step file form, and that form must work interactively.
func TestScanPipelineHintIsFileBased(t *testing.T) {
	args := []string{"scan", "-d", "merged-branch," + config.DetectorLogs}
	a := &app{args: args, goos: "linux"}
	got := a.applyCommand(leafFor(t, a, args))
	if strings.Contains(got, "| brooom clean") || strings.Contains(got, "--from -") {
		t.Errorf("hint still pipes into clean: %q", got)
	}
	if !strings.Contains(got, "--format json > brooom-findings.json") || !strings.Contains(got, "clean --from brooom-findings.json --apply") {
		t.Errorf("hint is not the file form: %q", got)
	}
}

func TestFileHintWorksWithAConfirmationPrompt(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	_, out, _ := brooom(t, "", "scan", "-d", "merged-branch,"+config.DetectorLogs, "--format", "json")
	file := writeRaw(t, out)
	code, _, errOut := runApp(t, "y\n", true, time.Time{}, "clean", "--from", file, "--apply", "--trash-strategy", "quarantine")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if f.hasBranch("feat/merged") {
		t.Errorf("clean did not act: %v", f.branches())
	}
}

func TestRerunHintFromStdinDoesNotSuggestPiping(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	data := mustJSON(t, scanReport(t))
	_, out, _ := clean(t, data, "--from", "-")
	if strings.Contains(out, "pipe them in again") {
		t.Errorf("stdin hint suggests the pipe again:\n%s", out)
	}
	if !strings.Contains(out, "--from <file> --apply") {
		t.Errorf("stdin hint lost the file form:\n%s", out)
	}
}

// TestApplyHintNamesOnlyWorkingShortcuts reproduces #215: the hint listed
// `brooom branches --apply` without the scope flags and without any branch
// finding, and suggested a sweep that does not cover the listed findings.
func TestApplyHintNamesOnlyWorkingShortcuts(t *testing.T) {
	cfg := config.Default()
	high := findings.ConfidenceHigh
	med := findings.ConfidenceMedium
	tests := []struct {
		name     string
		args     []string
		fs       []findings.Finding
		contains []string
		absent   []string
	}{
		{"scope flags on shortcut", []string{"scan", "-w", "--root", "/r"},
			[]findings.Finding{hintFinding("merged-branch", high, "b")},
			[]string{"`brooom sweep --workspaces --root /r --apply`", "`brooom branches --workspaces --root /r --apply`"},
			[]string{"brooom worktrees", "brooom logs"}},
		{"no branch findings", []string{"scan"},
			[]findings.Finding{hintFinding(config.DetectorLogs, high, "l")},
			[]string{"`brooom sweep --apply`", "`brooom logs --apply`"},
			[]string{"brooom branches"}},
		{"safe preset skips medium findings", []string{"scan"},
			[]findings.Finding{hintFinding("build-artifacts", med, "dist")},
			[]string{"safe preset", "brooom scan --format json > brooom-findings.json", "brooom clean --from brooom-findings.json --apply"},
			[]string{"`brooom sweep --apply`"}},
		{"partial coverage is stated", []string{"scan"},
			[]findings.Finding{hintFinding(config.DetectorLogs, high, "l"), hintFinding("build-artifacts", med, "dist")},
			[]string{"`brooom sweep --apply`", "covers up to 1 of 2", "brooom clean --from brooom-findings.json --apply"},
			nil},
		{"detector outside the preset", []string{"scan"},
			[]findings.Finding{hintFinding("git-bloat", high, "g")},
			[]string{"safe preset", "brooom clean --from brooom-findings.json --apply"},
			[]string{"`brooom sweep --apply`"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hintFor(t, cfg, tt.args, tt.fs...)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("hint lacks %q:\n%s", want, got)
				}
			}
			for _, bad := range tt.absent {
				if strings.Contains(got, bad) {
					t.Errorf("hint contains %q:\n%s", bad, got)
				}
			}
			for _, c := range hintCommands(got) {
				requireParses(t, c)
			}
		})
	}
}

// TestSweepHintFollowsConfiguredPreset: sweep.preset decides what a bare
// `sweep` covers, so the coverage claim has to use it.
func TestSweepHintFollowsConfiguredPreset(t *testing.T) {
	cfg := config.Default()
	cfg.Sweep.Preset = "standard"
	got := hintFor(t, cfg, []string{"scan"}, hintFinding("build-artifacts", findings.ConfidenceMedium, "dist"))
	if !strings.Contains(got, "`brooom sweep --apply`") {
		t.Errorf("standard preset covers medium findings:\n%s", got)
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
			code, _, errOut := clean(t, "", "--from", report, "-f", format, "--apply", "--yes")
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
	if code, _, errOut := clean(t, "", "--from", report); code != ExitOK {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestGitPurgeRejectsNonTableFormatWithOperations(t *testing.T) {
	newPurgeFixture(t, nil)
	for _, format := range []string{"json", "ndjson", "plain", "summary", "tree"} {
		t.Run(format, func(t *testing.T) {
			code, _, errOut := brooom(t, "", "git", "purge", "--prune", "now", "--format", format)
			if code != ExitUsage || !strings.Contains(errOut, "--format "+format) {
				t.Errorf("code %d, stderr %q", code, errOut)
			}
		})
	}
	if code, _, errOut := brooom(t, "", "git", "purge", "--prune", "now", "--format", "table"); code != ExitOK {
		t.Errorf("explicit table: code %d, stderr %q", code, errOut)
	}
}

func TestGitPurgeIgnoresConfigTreeFormat(t *testing.T) {
	newPurgeFixture(t, map[string]any{"output": map[string]any{"format": "tree"}})
	if code, _, errOut := brooom(t, "", "git", "purge", "--prune", "now"); code != ExitOK {
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
	acting := leafFor(t, a, []string{"branches", "--apply"})
	if got, _ := a.renderedFormat(acting, "ndjson"); !isTableFormat(got) {
		t.Errorf("an applying shortcut renders %q, want table", got)
	}
	explicit := &app{}
	if got, _ := explicit.renderedFormat(leafFor(t, explicit, []string{"branches", "-f", "json"}), ""); got != "json" {
		t.Errorf("explicit format lost: %q", got)
	}
}
