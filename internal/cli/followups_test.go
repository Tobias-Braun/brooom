package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/updatecheck"
)

// leafFor parses args against a fresh command tree bound to a and returns the
// command they select, like a real invocation would.
func leafFor(t *testing.T, a *app, args []string) *cobra.Command {
	t.Helper()
	root := newRootCmd(a)
	cmd, rest, err := root.Find(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags(rest); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// TestHintQuotingIsOSAware reproduces #182 items 3 and 12 for the CLI: a
// suggested command has to work in PowerShell and cmd.exe, where single
// quotes are wrong (cmd.exe), and on unix a `$` must not be left to the shell
// inside double quotes.
func TestHintQuotingIsOSAware(t *testing.T) {
	tests := []struct {
		goos, cfg, want string
	}{
		{"windows", `C:\my dir\c.json`, "run `brooom sweep --config \"C:\\my dir\\c.json\"`"},
		{"linux", `/my dir/c.json`, "run `brooom sweep --config '/my dir/c.json'`"},
		{"linux", `$HOME/c.json`, "run `brooom sweep --config '$HOME/c.json'`"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+" "+tt.cfg, func(t *testing.T) {
			args := []string{"scan", "--config", tt.cfg}
			a := &app{args: args, goos: tt.goos}
			a.flags.configPath = tt.cfg
			res := &scanResult{Report: &findings.Report{Findings: []findings.Finding{{
				Detector: "merged-branch", Confidence: findings.ConfidenceHigh,
				SuggestedAction: findings.SuggestedAction{Type: findings.ActionDeleteBranch},
			}}}}
			if got := a.scanHint(leafFor(t, a, args), res); !strings.Contains(got, tt.want) {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// TestHelpForUnknownTopicIsUsageError reproduces #182 item 4: `brooom help foo`
// printed the root help and exited 0.
func TestHelpForUnknownTopicIsUsageError(t *testing.T) {
	isolate(t)
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"unknown topic", []string{"help", "foo"}, ExitUsage},
		{"unknown nested topic", []string{"help", "config", "bogus"}, ExitUsage},
		{"known topic", []string{"help", "scan"}, ExitOK},
		{"known nested topic", []string{"help", "config", "show"}, ExitOK},
		{"bare help", []string{"help"}, ExitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := brooom(t, "", tt.args...)
			if code != tt.want {
				t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, tt.want, out, errOut)
			}
			if tt.want == ExitUsage && (!strings.Contains(errOut, "unknown help topic") || strings.Contains(out, "Usage:")) {
				t.Errorf("stdout %q stderr %q", out, errOut)
			}
			if tt.want == ExitOK && !strings.Contains(out, "Usage:") {
				t.Errorf("no help printed: %q", out)
			}
		})
	}
}

// TestEveryCommandRejectsUnknownArguments reproduces #182 item 8: the usage
// error test was a hand-picked table. This walks the whole command tree, so a
// new command that forgets its argument validator fails here.
func TestEveryCommandRejectsUnknownArguments(t *testing.T) {
	isolate(t)
	t.Chdir(t.TempDir())
	// These commands take positional arguments by design; they are checked
	// without any instead (they require at least one).
	variadic := map[string]bool{"brooom roots add": true, "brooom roots remove": true}
	seen := 0
	var walkTree func(c *cobra.Command)
	walkTree = func(c *cobra.Command) {
		if !c.Hidden {
			seen++
			path := strings.Fields(c.CommandPath())[1:]
			args := append(append([]string{}, path...), "bogus-1", "bogus-2", "bogus-3")
			if variadic[c.CommandPath()] {
				args = path
			}
			t.Run(c.CommandPath(), func(t *testing.T) {
				code, out, errOut := brooom(t, "", args...)
				if code != ExitUsage {
					t.Errorf("brooom %s: exit %d, want %d (stdout %q stderr %q)", strings.Join(args, " "), code, ExitUsage, out, errOut)
				}
			})
		}
		for _, sub := range c.Commands() {
			walkTree(sub)
		}
	}
	walkTree(NewRootCommand())
	if seen < 20 {
		t.Fatalf("only %d commands walked; the tree walk guards nothing", seen)
	}
}

// normalizeVolatile replaces what changes between runs (ages, session ids,
// temp paths) so outputs can be compared exactly.
func normalizeVolatile(out, repoDir string) string {
	out = strings.ReplaceAll(out, repoDir, "<repo>")
	out = regexp.MustCompile(`\d{8}-\d{6}-[0-9a-f]{4}`).ReplaceAllString(out, "<session>")
	out = regexp.MustCompile(`(\s)\d+(?:d|w|mo|y)(\s+(?:high|medium|low))`).ReplaceAllString(out, "${1}<age>${2}")
	return regexp.MustCompile(`\b[0-9a-f]{40}\b`).ReplaceAllString(out, "<sha>")
}

// TestQuietOutputIsExact reproduces #182 item 8: the quiet test compared
// output lengths, which passes for any shorter garbage.
func TestQuietOutputIsExact(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", sweepArgs("--dry-run", "-q")...)
	if code != ExitOK || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	want := "   -  <age>  high  delete-branch  .     feat/merged  no-remote\n" +
		"   -  <age>  high  delete-branch  .     feat/squash\n"
	if got := normalizeVolatile(out, f.repo.Dir); got != want {
		t.Errorf("quiet dry run:\n got %q\nwant %q", got, want)
	}

	// The full quiet summary is clean's; a quiet sweep is silent on success.
	report := writeRaw(t, func() string { _, o, _ := brooom(t, "", "scan", "-d", "merged-branch", "-f", "json"); return o }())
	code, out, errOut = brooom(t, "", "clean", "--from", report, "--yes", "-q")
	if code != ExitOK || errOut != "" {
		t.Fatalf("apply: code %d, stderr %q", code, errOut)
	}
	wantApply := "summary: 2 applied, 0 skipped, 0 failed\n" +
		"recovery hints:\n" +
		"  <repo> (feat/merged): run inside the repository: git branch feat/merged <sha>. " +
		"The commits are still reachable from main, so git gc will not prune them.\n" +
		"  <repo> (feat/squash): run inside the repository: git branch feat/squash <sha>. " +
		"The commits are still reachable from origin/feat/squash, so git gc will not prune them.\n" +
		"undo: brooom undo <session>\n"
	if got := normalizeVolatile(out, f.repo.Dir); got != wantApply {
		t.Errorf("quiet apply:\n got %q\nwant %q", got, wantApply)
	}
}

// TestSweepFormatsAndQuiet covers the sweep path of #111 (formats and
// --quiet), which had no tests.
func TestSweepFormatsAndQuiet(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	_, summary, _ := brooom(t, "", "sweep", "--dry-run", "-f", "summary")
	if !strings.Contains(summary, "DETECTOR") || !strings.Contains(summary, "dry run: nothing was changed") {
		t.Errorf("sweep -f summary:\n%s", summary)
	}
	_, table, _ := brooom(t, "", "sweep", "--dry-run", "-f", "table")
	if table == summary || !strings.Contains(table, "feat/merged") {
		t.Errorf("sweep -f table:\n%s", table)
	}
	code, quiet, errOut := brooom(t, "", "sweep", "--dry-run", "-q")
	if code != ExitOK || errOut != "" {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	want := "   -  <age>  high  delete-branch  .     feat/merged  no-remote\n" +
		"   -  <age>  high  delete-branch  .     feat/squash\n"
	if got := normalizeVolatile(quiet, f.repo.Dir); got != want {
		t.Errorf("sweep -q:\n got %q\nwant %q", got, want)
	}
}

// TestNothingSelectedPaths covers the nothing-selected path of #111: every
// detector of the command is disabled in the config, so no scan may start.
func TestNothingSelectedPaths(t *testing.T) {
	off := map[string]any{"enabled": false}
	cfg := map[string]any{"detectors": map[string]any{"merged-branch": off, "stale-branch": off}}
	tests := []struct {
		name    string
		args    []string
		wantOut string
		check   func(t *testing.T, out string)
	}{
		{"human", sweepArgs("--dry-run"), "nothing to clean\n", nil},
		{"quiet", sweepArgs("--dry-run", "-q"), "", nil},
		{"acting", sweepArgs("--yes"), "nothing to clean\n", nil},
		{"json is a valid empty report", sweepArgs("-f", "json"), "", func(t *testing.T, out string) {
			if !strings.Contains(out, `"findings": []`) {
				t.Errorf("no empty report:\n%s", out)
			}
		}},
		{"plain is empty", sweepArgs("-f", "plain"), "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCleanupFixture(t, cfg)
			code, out, errOut := brooom(t, "", tt.args...)
			if code != ExitOK || errOut != "" {
				t.Fatalf("code %d, stderr %q", code, errOut)
			}
			if tt.check != nil {
				tt.check(t, out)
			} else if out != tt.wantOut {
				t.Errorf("stdout %q, want %q", out, tt.wantOut)
			}
		})
	}
}

// TestActingRunHonoursExplicitFormat reproduces #182 item 5: an acting run
// silently ignored an explicit -f tree|table|summary.
func TestActingRunHonoursExplicitFormat(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", sweepArgs("--yes", "-f", "summary")...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "DETECTOR") {
		t.Errorf("-f summary was ignored by an acting run:\n%s", out)
	}
	if !strings.Contains(out, "2 merged branches removed") {
		t.Errorf("apply did not run:\n%s", out)
	}
}

func TestActingRunRejectsBadFormatBeforeActing(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	for _, format := range []string{"bogus", "json", "ndjson", "plain"} {
		code, _, errOut := brooom(t, "", sweepArgs("--yes", "-f", format)...)
		if code != ExitUsage {
			t.Errorf("-f %s: exit %d, want %d (stderr %q)", format, code, ExitUsage, errOut)
		}
	}
	if !f.hasBranch("feat/merged") || len(f.sessions()) != 0 {
		t.Error("a rejected format still acted")
	}
}

// TestUndoRejectsFormatAndDetector reproduces #182 item 6: undo accepted -f
// and -d and ignored them.
func TestUndoRejectsFormatAndDetector(t *testing.T) {
	newUndoFixture(t)
	tests := []struct {
		name string
		args []string
		want int
		msg  string
	}{
		{"format short", []string{"undo", "-f", "json"}, ExitUsage, "--format has no effect on 'undo'"},
		{"format long", []string{"undo", "--format", "plain"}, ExitUsage, "--format has no effect on 'undo'"},
		{"detector", []string{"undo", "-d", "build-artifacts"}, ExitUsage, "--detector has no effect on 'undo'"},
		{"path stays valid", []string{"undo", "--path", ".", "--dry-run"}, ExitOK, ""},
		{"plain undo", []string{"undo", "--dry-run"}, ExitOK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := brooom(t, "", tt.args...)
			if code != tt.want {
				t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, tt.want, out, errOut)
			}
			if tt.msg != "" && !strings.Contains(errOut, tt.msg) {
				t.Errorf("stderr %q lacks %q", errOut, tt.msg)
			}
		})
	}
}

// TestDetectorFailuresStayVisibleAndExitFour pins the rules of #182 item 7 and
// #191: exit 3 means no target was scanned, exit 4 means a detector failed
// inside a scanned scope. The failure is reported in every format.
func TestDetectorFailuresStayVisibleAndExitFour(t *testing.T) {
	failing := func(context.Context, *detect.Env, scope.Target, func(findings.Finding)) error {
		return errors.New("simulated failure")
	}
	for _, format := range []string{"json", "plain", "table", "summary"} {
		t.Run(format, func(t *testing.T) {
			newCleanupFixture(t, nil)
			d := registerFake(t, detect.CategoryFiles, failing)
			code, out, errOut := brooom(t, "", "scan", "-d", d.name, "-f", format)
			if code != ExitDetectorFailed {
				t.Errorf("exit %d, want %d (stdout %q stderr %q)", code, ExitDetectorFailed, out, errOut)
			}
			if !strings.Contains(out+errOut, "simulated failure") {
				t.Errorf("the failure is not reported anywhere:\nstdout %q\nstderr %q", out, errOut)
			}
		})
	}
}

// TestExitCodeRuleIsDocumented keeps the documented exit-3 rule and the
// behaviour above in step.
func TestExitCodeRuleIsDocumented(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "ARCHITECTURE.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(data)), " ")
	for _, want := range []string{
		"`3` no target was scanned",
		"`4` the scan ran and its report was written, but a detector failed",
		"Notes keep exit `0`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/ARCHITECTURE.md lacks %q", want)
		}
	}
}

// TestUpdateCheckSanitizesRemoteValues reproduces #182 item 10: the release
// URL and version come from the network and were printed raw.
func TestUpdateCheckSanitizesRemoteValues(t *testing.T) {
	newReleaseFixture(t, 200, `v2.0.0+\u001b[2Jpwn`, 0)
	a, out, errOut := newTestApp(t, "1.0.0")
	if code := execute(a, []string{"update-check"}); code != ExitOK {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Errorf("stdout carries a raw ESC: %q", out)
	}
	if !strings.Contains(out.String(), `\x1b`) {
		t.Errorf("escaped ESC missing: %q", out)
	}
}

func TestBackgroundNoticeSanitizesVersion(t *testing.T) {
	newReleaseFixture(t, 200, `v2.0.0+\u001b[2Jpwn`, 0)
	a, _, errOut := newTestApp(t, "1.0.0")
	enableBackground(a)
	a.update.grace = 5_000_000_000
	if code := execute(a, []string{"version"}); code != ExitOK {
		t.Fatalf("code %d", code)
	}
	if strings.ContainsRune(errOut.String(), 0x1b) {
		t.Errorf("stderr carries a raw ESC: %q", errOut)
	}
	if !strings.Contains(errOut.String(), "is available") {
		t.Errorf("no notice: %q", errOut)
	}
	_ = updatecheck.BaseURLEnv
}

// TestExplicitConfigErrorIsSanitized reproduces #182 item 11: the missing
// --config path went into the error unescaped.
func TestExplicitConfigErrorIsSanitized(t *testing.T) {
	isolate(t)
	// Windows rejects the C0 controls in file names (Stat then fails with an
	// invalid-name error instead of not-exist), so it gets a C1 control, which
	// is a legal name character there but just as unsafe to print raw.
	name := "/no\x1b[31msuch\nfake.json"
	if runtime.GOOS == "windows" {
		name = "/no\u0085such fake.json"
	}
	bad := t.TempDir() + name
	a := &app{}
	a.flags.configPath = bad
	err := a.requireExplicitConfig(bad)
	if err == nil {
		t.Fatal("no error for a missing config")
	}
	for _, r := range err.Error() {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == ' ' {
			t.Fatalf("control rune %q in %q", r, err)
		}
	}
	if _, statErr := os.Stat(bad); statErr == nil {
		t.Fatal("test path exists")
	}
}
