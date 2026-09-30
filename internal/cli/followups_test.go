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

// TestApplyHintDropsYesAndFormat reproduces #182 item 1: a pasted hint kept
// -y (so it skipped the confirmation) and an explicit --format (so a machine
// format made --apply fail).
func TestApplyHintDropsYesAndFormat(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"short yes", []string{"branches", "-y"}, "brooom branches --apply"},
		{"long yes", []string{"branches", "--yes", "--merged"}, "brooom branches --merged --apply"},
		{"yes with value", []string{"branches", "--yes=true"}, "brooom branches --apply"},
		{"short format", []string{"branches", "-f", "tree"}, "brooom branches --apply"},
		{"long format", []string{"branches", "--format", "json"}, "brooom branches --apply"},
		{"format equals", []string{"branches", "--format=json"}, "brooom branches --apply"},
		{"attached short format", []string{"branches", "-fjson"}, "brooom branches --apply"},
		{"format before command", []string{"-f", "plain", "branches"}, "brooom branches --apply"},
		{"cluster with yes", []string{"branches", "-qy"}, "brooom branches -q --apply"},
		{"cluster ending in format", []string{"branches", "-qf", "json"}, "brooom branches -q --apply"},
		{"other flags stay", []string{"sweep", "-y", "-d", "merged-branch", "-f", "table", "--trash-strategy", "quarantine"},
			"brooom sweep -d merged-branch --trash-strategy quarantine --apply"},
		{"clean from file", []string{"clean", "--from", "f.json", "--yes", "--format", "json"}, "brooom clean --from f.json --apply"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{args: tt.args}
			cmd := leafFor(t, a, tt.args)
			if cmd.Name() == "brooom" {
				cmd, _, _ = cmd.Find(cmd.Flags().Args())
			}
			if got := a.applyCommand(cmd); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			requireParses(t, a.applyCommand(cmd))
		})
	}
}

// TestScanHintKeepsForce reproduces #182 item 2: findings of a `scan --force`
// are only reproduced by a command that forces as well.
func TestScanHintKeepsForce(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"sweep", []string{"scan", "--force"}, "brooom sweep --force --apply"},
		{"scope flags", []string{"scan", "--force", "-w", "--root", "/r"}, "brooom sweep --workspaces --root /r --force --apply"},
		{"shortcut", []string{"scan", "--force", "-d", "merged-branch"}, "brooom branches --detector merged-branch --force --apply"},
		{"pipeline forces both ends", []string{"scan", "--force", "-d", "git-bloat,logs"},
			"brooom scan --detector " + findings.Quote("git-bloat,logs") + " --force --format json > brooom-findings.json && brooom clean --from brooom-findings.json --force --apply"},
		{"without force nothing is added", []string{"scan"}, "brooom sweep --apply"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{args: tt.args}
			got := a.applyCommand(leafFor(t, a, tt.args))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			for _, c := range hintCommands("`" + got + "`") {
				requireParses(t, c)
			}
		})
	}
}

// TestHintQuotingIsOSAware reproduces #182 items 3 and 12 for the CLI: the
// pipeline hint has to work in PowerShell and cmd.exe, where single quotes
// are wrong (cmd.exe) or expand nothing useful, and on unix a `$` must not be
// left to the shell inside double quotes.
func TestHintQuotingIsOSAware(t *testing.T) {
	tests := []struct {
		name string
		goos string
		want string
	}{
		{"windows path with a space", "windows",
			`brooom scan --config "C:\my dir\c.json" --detector "git-bloat,logs" --format json > brooom-findings.json && brooom clean --config "C:\my dir\c.json" --from brooom-findings.json --apply`},
		{"linux path with a space", "linux",
			`brooom scan --config '/my dir/c.json' --detector git-bloat,logs --format json > brooom-findings.json && brooom clean --config '/my dir/c.json' --from brooom-findings.json --apply`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := `/my dir/c.json`
			if tt.goos == "windows" {
				cfg = `C:\my dir\c.json`
			}
			args := []string{"scan", "--config", cfg, "-d", "git-bloat,logs"}
			a := &app{args: args, goos: tt.goos}
			if got := a.applyCommand(leafFor(t, a, args)); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestHintQuotingKeepsDollarLiteral(t *testing.T) {
	a := &app{args: []string{"clean", "--from", "$HOME/x y.json"}, goos: "linux"}
	got := a.applyCommand(leafFor(t, a, a.args))
	if want := `brooom clean --from '$HOME/x y.json' --apply`; got != want {
		t.Errorf("got %q, want %q", got, want)
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
	code, out, errOut := brooom(t, "", "branches", "-q")
	if code != ExitOK || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	want := "   -  <age>  high  delete-branch  .     feat/merged  no-remote\n" +
		"   -  <age>  high  delete-branch  .     feat/squash\n"
	if got := normalizeVolatile(out, f.repo.Dir); got != want {
		t.Errorf("quiet dry run:\n got %q\nwant %q", got, want)
	}

	code, out, errOut = brooom(t, "", "branches", "--apply", "--yes", "-q")
	if code != ExitOK || errOut != "" {
		t.Fatalf("apply: code %d, stderr %q", code, errOut)
	}
	wantApply := "summary: 2 applied, 0 skipped, 0 failed\n" +
		"recovery hints:\n" +
		"  <repo> (feat/merged): run inside the repository: git branch feat/merged <sha>. " +
		"The commits are still reachable from feat/squash, so git gc will not prune them.\n" +
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
	_, summary, _ := brooom(t, "", "sweep", "-f", "summary")
	if !strings.Contains(summary, "DETECTOR") || !strings.Contains(summary, "dry run: nothing was changed") {
		t.Errorf("sweep -f summary:\n%s", summary)
	}
	_, table, _ := brooom(t, "", "sweep", "-f", "table")
	if table == summary || !strings.Contains(table, "feat/merged") {
		t.Errorf("sweep -f table:\n%s", table)
	}
	code, quiet, errOut := brooom(t, "", "sweep", "-q")
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
		{"human", []string{"branches"}, "nothing to clean\n", nil},
		{"quiet", []string{"branches", "-q"}, "", nil},
		{"apply", []string{"branches", "--apply", "--yes"}, "nothing to clean\n", nil},
		{"json is a valid empty report", []string{"branches", "-f", "json"}, "", func(t *testing.T, out string) {
			if !strings.Contains(out, `"findings": []`) {
				t.Errorf("no empty report:\n%s", out)
			}
		}},
		{"plain is empty", []string{"branches", "-f", "plain"}, "", nil},
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

// TestApplyHonoursExplicitFormat reproduces #182 item 5: with --apply an
// explicit -f tree|table|summary was silently ignored.
func TestApplyHonoursExplicitFormat(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", "branches", "--apply", "--yes", "-f", "summary")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "DETECTOR") {
		t.Errorf("-f summary was ignored with --apply:\n%s", out)
	}
	if !strings.Contains(out, "summary: 2 applied") {
		t.Errorf("apply did not run:\n%s", out)
	}
}

func TestApplyRejectsBadFormatBeforeActing(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	for _, format := range []string{"bogus", "json", "ndjson", "plain"} {
		code, _, errOut := brooom(t, "", "branches", "--apply", "--yes", "-f", format)
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
		{"workspaces stays valid", []string{"undo", "--workspaces"}, ExitOK, ""},
		{"plain undo", []string{"undo"}, ExitOK, ""},
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

// TestDetectorFailuresStayVisibleAndExitZero pins the rule chosen for #182
// item 7: exit 3 means no target was scanned. A detector that fails inside a
// scanned scope is reported in every format but keeps exit 0, because the
// engine cannot tell a failed detector from one that returned a non-fatal note
// (a linked worktree outside the scope, for example), and failing every such
// scan would make notes fatal.
func TestDetectorFailuresStayVisibleAndExitZero(t *testing.T) {
	failing := func(context.Context, *detect.Env, scope.Target, func(findings.Finding)) error {
		return errors.New("simulated failure")
	}
	for _, format := range []string{"json", "plain", "table", "summary"} {
		t.Run(format, func(t *testing.T) {
			newCleanupFixture(t, nil)
			d := registerFake(t, detect.CategoryFiles, failing)
			code, out, errOut := brooom(t, "", "scan", "-d", d.name, "-f", format)
			if code != ExitOK {
				t.Errorf("exit %d, want %d (stdout %q stderr %q)", code, ExitOK, out, errOut)
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
		"A detector that fails inside a scanned scope is reported but stays `0`",
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
