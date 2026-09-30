package cli

import (
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// shellSplit splits a suggested command line into arguments the way the host
// shell would: whitespace separates, single quotes are literal (POSIX closes
// and reopens around a quote, PowerShell doubles it), double quotes group
// without escapes, which is all findings.Quote ever emits.
func shellSplit(t *testing.T, line string) []string {
	t.Helper()
	windows := runtime.GOOS == "windows"
	var out []string
	var word strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			out = append(out, word.String())
		}
		word.Reset()
		inWord = false
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == ' ' || r == '\t':
			flush()
		case r == '\'':
			inWord = true
			for i++; i < len(rs); i++ {
				if rs[i] == '\'' {
					if windows && i+1 < len(rs) && rs[i+1] == '\'' {
						word.WriteRune('\'')
						i++
						continue
					}
					break
				}
				word.WriteRune(rs[i])
			}
			if i >= len(rs) {
				t.Fatalf("unterminated quote in %q", line)
			}
		case r == '"':
			inWord = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				word.WriteRune(rs[i])
			}
			if i >= len(rs) {
				t.Fatalf("unterminated quote in %q", line)
			}
		case r == '\\' && !windows && i+1 < len(rs):
			inWord = true
			i++
			word.WriteRune(rs[i])
		default:
			inWord = true
			word.WriteRune(r)
		}
	}
	flush()
	return out
}

// requireParses parses one `brooom ...` command against the real command
// tree, flags included, without running it.
func requireParses(t *testing.T, command string) {
	t.Helper()
	args := shellSplit(t, command)
	if len(args) == 0 || args[0] != "brooom" {
		t.Errorf("not a brooom command: %q", command)
		return
	}
	root := newRootCmd(&app{})
	cmd, rest, err := root.Find(args[1:])
	if err != nil {
		t.Errorf("%q: %v", command, err)
		return
	}
	if err := cmd.ParseFlags(rest); err != nil {
		t.Errorf("%q does not parse: %v", command, err)
	}
}

// backtickCommands returns every `brooom ...` span of text, split at pipes.
var (
	backtick = regexp.MustCompile("`(brooom [^`]+)`")
	quoted   = regexp.MustCompile(`'(brooom [^']+)'`)
)

func hintCommands(text string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{backtick, quoted} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			for _, pipe := range strings.Split(m[1], "|") {
				for _, part := range strings.Split(pipe, "&&") {
					out = append(out, strings.TrimSpace(part))
				}
			}
		}
	}
	return out
}

// hintFinding is an actionable finding of a detector at a confidence.
func hintFinding(detector string, c findings.Confidence) findings.Finding {
	return findings.Finding{
		Detector: detector, Confidence: c,
		SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash},
	}
}

func TestScanHint(t *testing.T) {
	merged := hintFinding("merged-branch", findings.ConfidenceHigh)
	stale := hintFinding("stale-branch", findings.ConfidenceHigh)
	logs := hintFinding(config.DetectorLogs, findings.ConfidenceMedium)
	activeBuild := hintFinding("build-artifacts", findings.ConfidenceMedium)
	tests := []struct {
		name   string
		args   []string
		preset string
		fs     []findings.Finding
		want   string
	}{
		{"default preset", []string{"scan"}, "", []findings.Finding{merged, logs},
			"nothing was changed; run `brooom sweep` to review and clean these"},
		{"bare command", nil, "", []findings.Finding{merged},
			"nothing was changed; run `brooom sweep` to review and clean these"},
		{"scope flags", []string{"scan", "-w", "--root", "/r", "--config", "c.json"}, "", []findings.Finding{merged},
			"nothing was changed; run `brooom sweep --config c.json --workspaces --root /r` to review and clean these"},
		{"detector kept when the preset runs it", []string{"scan", "-d", "merged-branch,stale-branch"}, "", []findings.Finding{merged, stale},
			"nothing was changed; run `brooom sweep --detector merged-branch` to review and clean these (1 of 2; the rest are in no sweep preset)"},
		{"configured preset covers nothing", []string{"scan"}, "tidy", []findings.Finding{merged},
			"nothing was changed; run `brooom sweep everything` to review and clean these"},
		{"legacy preset in the config", []string{"scan"}, "safe", []findings.Finding{merged},
			"nothing was changed; run `brooom sweep` to review and clean these"},
		{"no preset acts", []string{"scan"}, "", []findings.Finding{stale, activeBuild},
			"nothing was changed; no sweep preset acts on these findings (see `brooom help sweep`)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{args: tt.args, goos: "linux"}
			root := newRootCmd(a)
			if err := root.ParseFlags(tt.args); err != nil {
				t.Fatal(err)
			}
			cmd, rest, _ := root.Find(root.Flags().Args())
			if err := cmd.ParseFlags(rest); err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			if tt.preset != "" {
				cfg.Sweep.Preset = tt.preset
			}
			res := &scanResult{Config: cfg, Report: &findings.Report{Findings: tt.fs}}
			got := a.scanHint(cmd, res)
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
			for _, c := range hintCommands(got) {
				requireParses(t, c)
			}
		})
	}
}

// TestScanHintOnlyAfterScan: commands that render a scan report for other
// reasons (git purge, a machine-format sweep) print their own guidance.
func TestScanHintOnlyAfterScan(t *testing.T) {
	a := &app{}
	root := newRootCmd(a)
	cmd, _, err := root.Find([]string{"git", "purge"})
	if err != nil {
		t.Fatal(err)
	}
	res := &scanResult{Config: config.Default(), Report: &findings.Report{Findings: []findings.Finding{hintFinding("git-bloat", findings.ConfidenceMedium)}}}
	if got := a.scanHint(cmd, res); got != "" {
		t.Errorf("git purge got a scan hint: %q", got)
	}
}

// TestHintsRoundTripThroughTheRealCommands runs a scan, reads the suggested
// command out of the output and checks that it parses and does what the scan
// reported: the merged branches, and nothing more.
func TestHintsRoundTripThroughTheRealCommands(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	f.feature("feat/unmerged")

	_, out, _ := brooom(t, "", "scan", "--detector", "merged-branch")
	cmds := hintCommands(out)
	if len(cmds) != 1 || cmds[0] != "brooom sweep --detector merged-branch" {
		t.Fatalf("hint commands %q in:\n%s", cmds, out)
	}
	requireParses(t, cmds[0])

	// Executing the suggested command deletes what the scan reported.
	args := append(shellSplit(t, cmds[0])[1:], "--yes")
	if code, _, errOut := brooom(t, "", args...); code != ExitOK {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if f.hasBranch("feat/merged") || !f.hasBranch("feat/unmerged") {
		t.Errorf("branches after sweep: %v", f.branches())
	}
}

func TestCleanHintsReplayable(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	path := writeReportFile(t, scanReport(t).Findings...)

	_, out, _ := clean(t, "", "--from", path, "--dry-run")
	if !strings.Contains(out, "dry run: nothing was changed; re-run without --dry-run to execute") {
		t.Fatalf("file hint:\n%s", out)
	}

	// stdin cannot be replayed: the hint says so instead of suggesting a
	// command that fails with an empty input.
	data := mustJSON(t, scanReport(t))
	_, out, _ = clean(t, data, "--from", "-", "--dry-run")
	if !strings.Contains(out, "save the findings to a file") || strings.Contains(out, "re-run without") {
		t.Errorf("stdin hint:\n%s", out)
	}
}

// TestForceHintsParse pins #126: every `brooom ... --force` in help and hint
// text must be a command that exists. `scan --force` did not, and
// `brooom <command> --force` cannot be run at all.
func TestForceHintsParse(t *testing.T) {
	texts := []string{forceNoneHint}
	root := newRootCmd(&app{})
	for _, c := range append(root.Commands(), root) {
		texts = append(texts, c.Long, c.Example)
	}
	forceCmd := regexp.MustCompile("brooom [^\\n'`|]*--force[^\\n'`|]*")
	seen := 0
	for _, text := range texts {
		for _, m := range forceCmd.FindAllString(text, -1) {
			seen++
			requireParses(t, strings.TrimSpace(m))
		}
	}
	if seen == 0 {
		t.Fatal("no `brooom ... --force` text found; the test guards nothing")
	}
}

func TestScanForceIsReadOnlyAndReachesDetectors(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, _, errOut := brooom(t, "", "scan", "--force", "--format", "json")
	if code != ExitOK {
		t.Fatalf("scan --force: code %d, stderr %q", code, errOut)
	}
	if !f.hasBranch("feat/merged") || len(f.sessions()) != 0 {
		t.Error("scan --force changed something")
	}
}

// TestDetectorFlagIsOneWordOnWindows guards the multi-detector hint: ',' is
// PowerShell's array operator, so a bare `a,b` would reach the exe as two
// arguments and the pasted hint would sweep the wrong detectors.
func TestDetectorFlagIsOneWordOnWindows(t *testing.T) {
	everything := mustPreset("everything")
	a := &app{goos: "windows"}
	a.flags.detectors = []string{"build-artifacts", "merged-branch"}
	got := a.presetDetectorFlag(everything)
	want := []string{"--detector", `"build-artifacts,merged-branch"`}
	if !slices.Equal(got, want) {
		t.Errorf("presetDetectorFlag() = %q, want %q", got, want)
	}
	u := &app{goos: "linux"}
	u.flags.detectors = a.flags.detectors
	if got := u.presetDetectorFlag(everything); got[1] != "build-artifacts,merged-branch" {
		t.Errorf("unix presetDetectorFlag() = %q, want a bare word", got)
	}
}
