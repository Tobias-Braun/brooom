package cli

import (
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

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
			for _, part := range strings.Split(m[1], "|") {
				out = append(out, strings.TrimSpace(part))
			}
		}
	}
	return out
}

func TestApplyCommandKeepsTheInvocation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"merged flag", []string{"branches", "--merged"}, "brooom branches --merged --apply"},
		{"sweep with detector and strategy", []string{"sweep", "-d", "merged-branch", "--trash-strategy", "delete"},
			"brooom sweep -d merged-branch --trash-strategy delete --apply"},
		{"clean from file", []string{"clean", "--from", "f.json"}, "brooom clean --from f.json --apply"},
		{"existing apply is not doubled", []string{"branches", "--apply", "--merged"}, "brooom branches --merged --apply"},
		{"apply=true is replaced", []string{"branches", "--apply=false"}, "brooom branches --apply"},
		{"spaces are quoted", []string{"clean", "--from", "my findings.json"}, "brooom clean --from " + findings.Quote("my findings.json") + " --apply"},
		{"global flags before the command", []string{"--config", "c.json", "-w", "logs"}, "brooom --config c.json -w logs --apply"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{args: tt.args}
			root := newRootCmd(a)
			cmd, _, err := root.Find(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			// Find skips leading global flags poorly, so resolve the leaf
			// through a full parse for those.
			if cmd == root {
				if err := root.ParseFlags(tt.args); err != nil {
					t.Fatal(err)
				}
				cmd, _, _ = root.Find(root.Flags().Args())
			}
			if got := a.applyCommand(cmd); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			requireParses(t, a.applyCommand(cmd))
		})
	}
}

func TestScanApplyHints(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"plain", []string{"scan"}, "brooom sweep --apply"},
		{"scope flags", []string{"scan", "-w", "--root", "/r", "--config", "c.json"}, "brooom sweep --config c.json --workspaces --root /r --apply"},
		{"one shortcut", []string{"scan", "-d", "merged-branch"}, "brooom branches --detector merged-branch --apply"},
		{"two detectors of one shortcut", []string{"scan", "-d", "stale-branch,merged-branch"}, "brooom branches --detector stale-branch,merged-branch --apply"},
		{"detectors of no shortcut", []string{"scan", "-d", "git-bloat,logs"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{args: tt.args}
			root := newRootCmd(a)
			cmd, rest, _ := root.Find(tt.args)
			if err := cmd.ParseFlags(rest); err != nil {
				t.Fatal(err)
			}
			got := a.applyCommand(cmd)
			if tt.want != "" && got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if tt.want == "" && (!strings.Contains(got, "brooom scan --detector git-bloat,logs --format json | brooom clean --from - --apply")) {
				t.Errorf("got %q", got)
			}
			for _, c := range hintCommands("`" + got + "`") {
				requireParses(t, c)
			}
		})
	}
}

// TestHintsRoundTripThroughTheRealCommands runs commands, reads the suggested
// command out of the output and checks that it parses and does what the dry
// run promised: the reported scope, and nothing more.
func TestHintsRoundTripThroughTheRealCommands(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	f.feature("feat/unmerged")

	_, out, _ := brooom(t, "", "branches", "--merged", "--detector", "merged-branch")
	cmds := hintCommands(out)
	if len(cmds) == 0 {
		t.Fatalf("no hint in:\n%s", out)
	}
	for _, c := range cmds {
		requireParses(t, c)
	}
	if !strings.Contains(out, "brooom branches --merged --detector merged-branch --apply") {
		t.Errorf("hint lost the invocation:\n%s", out)
	}

	// Executing the suggested command deletes what the dry run reported.
	args := shellSplit(t, "brooom branches --merged --detector merged-branch --apply --yes")[1:]
	if code, _, errOut := brooom(t, "", args...); code != ExitOK {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if f.hasBranch("feat/merged") || !f.hasBranch("feat/unmerged") {
		t.Errorf("branches after apply: %v", f.branches())
	}
}

func TestCleanHintsReplayable(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	path := writeReportFile(t, scanReport(t).Findings...)

	_, out, _ := clean(t, "", "--from", path)
	if !strings.Contains(out, "re-run 'brooom clean --from "+path+" ") {
		t.Fatalf("file hint lost --from:\n%s", out)
	}
	for _, c := range hintCommands(out) {
		requireParses(t, c)
	}

	// stdin cannot be replayed: the hint says so instead of suggesting a
	// command that fails with an empty input.
	data := mustJSON(t, scanReport(t))
	_, out, _ = clean(t, data, "--from", "-")
	if !strings.Contains(out, "save the findings to a file") || strings.Contains(out, "re-run 'brooom clean") {
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
// arguments and the pasted hint would scan the wrong detectors.
func TestDetectorFlagIsOneWordOnWindows(t *testing.T) {
	a := &app{goos: "windows"}
	a.flags.detectors = []string{"stale-branch", "merged-branch"}
	got := a.detectorFlag()
	want := []string{"--detector", `"stale-branch,merged-branch"`}
	if !slices.Equal(got, want) {
		t.Errorf("detectorFlag() = %q, want %q", got, want)
	}
	u := &app{goos: "linux"}
	u.flags.detectors = a.flags.detectors
	if got := u.detectorFlag(); got[1] != "stale-branch,merged-branch" {
		t.Errorf("unix detectorFlag() = %q, want a bare word", got)
	}
}
