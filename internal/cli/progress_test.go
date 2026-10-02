package cli

import (
	"bytes"
	"context"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShowProgressMatrix(t *testing.T) {
	// The base is a person at an ordinary terminal running a human format.
	base := progressInputs{format: "table", stderrTTY: true, term: "xterm-256color"}
	with := func(mod func(*progressInputs)) progressInputs {
		in := base
		mod(&in)
		return in
	}
	tests := []struct {
		name string
		in   progressInputs
		want bool
	}{
		{"on a terminal", base, true},
		{"tree", with(func(i *progressInputs) { i.format = "tree" }), true},
		{"summary", with(func(i *progressInputs) { i.format = "summary" }), true},
		{"stderr is not a terminal", with(func(i *progressInputs) { i.stderrTTY = false }), false},
		{"quiet", with(func(i *progressInputs) { i.quiet = true }), false},
		{"CI set", with(func(i *progressInputs) { i.ci = "true" }), false},
		{"CI set to any value", with(func(i *progressInputs) { i.ci = "0" }), false},
		{"TERM dumb", with(func(i *progressInputs) { i.term = "dumb" }), false},
		{"TERM unset is not dumb", with(func(i *progressInputs) { i.term = "" }), true},
		{"json", with(func(i *progressInputs) { i.format = "json" }), false},
		{"ndjson", with(func(i *progressInputs) { i.format = "ndjson" }), false},
		{"plain", with(func(i *progressInputs) { i.format = "plain" }), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := showProgress(tc.in); got != tc.want {
				t.Errorf("showProgress(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// runTTY runs the CLI with a stderr that claims to be a terminal, so the
// auto detection can be exercised without one. env are extra variables; the
// ones the decision reads are reset first, so the developer's own terminal
// and CI settings never leak in.
func runTTY(t *testing.T, tty bool, env map[string]string, args ...string) (code int, out, errOut string) {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	for k, v := range env {
		t.Setenv(k, v)
	}
	var o, e bytes.Buffer
	a := &app{io: IO{In: strings.NewReader(""), Out: &o, Err: &e}, stderrTTY: func() bool { return tty }}
	code = execute(a, args)
	return code, o.String(), e.String()
}

// displayMarks are what only the live display writes to stderr.
var displayMarks = []string{"\x1b[?25l", "✓ done", "Scanning", "Discovering"}

func TestScanShowsSummaryOnATerminal(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := runTTY(t, true, nil, "sweep", "after-agents", "-d", "merged-branch", "--dry-run")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"✓ done · discover, scan, plan", "findings in "} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%q", want, errOut)
		}
	}
	// The results keep going to stdout, untouched by the display.
	if !strings.Contains(out, "feat/merged") || strings.Contains(out, "\x1b") || strings.Contains(out, "✓ done") {
		t.Errorf("stdout is not the plain report:\n%q", out)
	}
	if !strings.HasSuffix(errOut, "\n") {
		t.Errorf("stderr must end with a complete line: %q", errOut)
	}
}

func TestNoDisplayWhenAutoConditionsFail(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	tests := []struct {
		name string
		tty  bool
		env  map[string]string
		args []string
	}{
		{"stderr not a terminal", false, nil, []string{"sweep", "--dry-run"}},
		{"CI", true, map[string]string{"CI": "true"}, []string{"sweep", "--dry-run"}},
		{"TERM dumb", true, map[string]string{"TERM": "dumb"}, []string{"sweep", "--dry-run"}},
		{"quiet", true, nil, []string{"sweep", "after-agents", "-d", "merged-branch", "--dry-run", "--quiet"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := runTTY(t, tc.tty, tc.env, tc.args...)
			if code != ExitOK {
				t.Fatalf("code %d, stderr %q", code, errOut)
			}
			for _, m := range displayMarks {
				if strings.Contains(errOut, m) {
					t.Errorf("stderr contains display output %q: %q", m, errOut)
				}
			}
		})
	}
}

func TestNoColorKeepsTheDisplayButDropsColour(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, _, errOut := runTTY(t, true, map[string]string{"NO_COLOR": "1"}, "sweep", "after-agents", "-d", "merged-branch", "--dry-run")
	if code != ExitOK || !strings.Contains(errOut, "✓ done") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if regexp.MustCompile(`\x1b\[[0-9;]*m`).MatchString(errOut) {
		t.Errorf("NO_COLOR still produced colour sequences: %q", errOut)
	}
}

// generatedAt is the one field of a json report that differs between runs.
var generatedAt = regexp.MustCompile(`"generated_at": "[^"]*"`)

// TestMachineFormatsAreUnchangedOnAFakedTerminal is the guarantee of the
// machine formats: however terminal-like stderr looks, their stdout is
// byte-identical to a run without a terminal, and stderr carries no display
// output.
func TestMachineFormatsAreUnchangedOnAFakedTerminal(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	for _, format := range []string{"json", "ndjson", "plain"} {
		for _, cmd := range [][]string{{"sweep"}, {"sweep", "--dry-run"}} {
			args := append(append([]string{}, cmd...), "-f", format)
			refCode, refOut, refErr := runTTY(t, false, nil, args...)
			if refCode != ExitOK || refOut == "" {
				t.Fatalf("%v: reference run failed: code %d, out %q, stderr %q", args, refCode, refOut, refErr)
			}
			code, out, errOut := runTTY(t, true, nil, args...)
			if code != refCode {
				t.Errorf("%v: code %d, want %d", args, code, refCode)
			}
			if got, want := generatedAt.ReplaceAllString(out, ""), generatedAt.ReplaceAllString(refOut, ""); got != want {
				t.Errorf("%v: stdout differs from the run without a terminal\n got: %q\nwant: %q", args, got, want)
			}
			if errOut != refErr {
				t.Errorf("%v: stderr differs from the run without a terminal\n got: %q\nwant: %q", args, errOut, refErr)
			}
		}
	}
}

// TestConfigMachineFormatAlsoDisablesTheDisplay covers a machine format that
// only comes from output.format in the config: the resolved format decides.
func TestConfigMachineFormatAlsoDisablesTheDisplay(t *testing.T) {
	f := newCleanupFixture(t, map[string]any{"output": map[string]any{"format": "ndjson"}})
	f.mergedAndSquashed()
	code, out, errOut := runTTY(t, true, nil, "sweep", "--dry-run")
	if code != ExitOK || !strings.Contains(out, `"kind"`) {
		t.Fatalf("code %d, stdout %q", code, out)
	}
	for _, m := range displayMarks {
		if strings.Contains(errOut, m) {
			t.Errorf("stderr contains display output %q: %q", m, errOut)
		}
	}
}

func TestApplyShowsProgressAndStillDeletes(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := runTTY(t, true, nil, "sweep", "after-agents", "-d", "merged-branch", "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if f.hasBranch("feat/merged") {
		t.Errorf("the branch was not deleted: %v", f.branches())
	}
	if !strings.Contains(errOut, "✓ done · discover, scan, plan, apply") {
		t.Errorf("stderr lacks the summary of all four phases: %q", errOut)
	}
	if !strings.Contains(out, "2 merged branches removed") || strings.Contains(out, "\x1b") {
		t.Errorf("the executor summary must stay on plain stdout: %q", out)
	}
}

// TestApplyWithConfigMachineFormatStillShowsProgress: an applying run prints
// human text whatever the config says, so it may show the display.
func TestApplyWithConfigMachineFormatStillShowsProgress(t *testing.T) {
	f := newCleanupFixture(t, map[string]any{"output": map[string]any{"format": "json"}})
	f.mergedAndSquashed()
	code, _, errOut := runTTY(t, true, nil, "sweep", "after-agents", "-d", "merged-branch", "--yes")
	if code != ExitOK || !strings.Contains(errOut, "✓ done") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestConfirmationPromptsAreNotShadowedByTheDisplay(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	var o, e bytes.Buffer
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	a := &app{
		io:        IO{In: strings.NewReader("y\ny\ny\ny\n"), Out: &o, Err: &e},
		stdinTTY:  func() bool { return true },
		stderrTTY: func() bool { return true },
	}
	if code := execute(a, []string{"sweep", "after-agents", "-d", "merged-branch"}); code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, e.String())
	}
	// The prompt and the plan are on stdout, and the answers were consumed:
	// the display takes no input, so it cannot steal the scripted stdin.
	if !strings.Contains(o.String(), "[") || f.hasBranch("feat/merged") {
		t.Errorf("the prompt was not answered; branches %v, stdout %q", f.branches(), o.String())
	}
	if strings.Contains(o.String(), "✓ done") {
		t.Errorf("display output leaked into stdout: %q", o.String())
	}
}

func TestUndoShowsProgress(t *testing.T) {
	f := newUndoFixture(t)
	f.session(sid1, time.Now(), f.write("a.txt", "a"))
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	var o, e bytes.Buffer
	a := &app{io: IO{In: strings.NewReader(""), Out: &o, Err: &e}, stderrTTY: func() bool { return true }}
	if code := execute(a, []string{"undo", "--yes"}); code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, o.String(), e.String())
	}
	if !strings.Contains(e.String(), "✓ done · undo") {
		t.Errorf("stderr lacks the undo summary: %q", e.String())
	}
	if !strings.Contains(o.String(), "1 restored") {
		t.Errorf("stdout lacks the undo summary: %q", o.String())
	}
}

func TestUndoIgnoresAMachineFormatFromTheConfig(t *testing.T) {
	f := newUndoFixture(t)
	f.session(sid1, time.Now(), f.write("a.txt", "a"))
	writeConfig(t, f.home, map[string]any{"output": map[string]any{"format": "json"}})
	code, _, errOut := runTTY(t, true, nil, "undo", "--yes")
	if code != ExitOK || !strings.Contains(errOut, "✓ done · undo") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestErrorTextComesAfterTheSummaryAndTheTerminalIsRestored(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	// An unknown session makes undo fail after the flag handling; a failed
	// run must not leave the display behind: no cursor left hidden.
	code, _, errOut := runTTY(t, true, nil, "undo", "nonexistent")
	if code == ExitOK {
		t.Fatal("expected an error")
	}
	if strings.Contains(errOut, "\x1b[?25l") && strings.LastIndex(errOut, "\x1b[?25h") < strings.LastIndex(errOut, "\x1b[?25l") {
		t.Errorf("cursor left hidden: %q", errOut)
	}
}

// TestCancelledScanRestoresTheTerminal covers Ctrl-C (#219): the context is
// cancelled, the scan returns its partial report and the display is stopped
// with the failure summary.
func TestCancelledScanRestoresTheTerminal(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var o, e bytes.Buffer
	a := &app{io: IO{In: strings.NewReader(""), Out: &o, Err: &e}, stderrTTY: func() bool { return true }}
	code := executeContext(ctx, a, []string{"sweep", "--dry-run"})
	if code != ExitError {
		t.Fatalf("code %d, want %d", code, ExitError)
	}
	got := e.String()
	if !strings.Contains(got, "✗ stopped") {
		t.Errorf("no failure summary: %q", got)
	}
	if hide, show := strings.LastIndex(got, "\x1b[?25l"), strings.LastIndex(got, "\x1b[?25h"); hide > show {
		t.Errorf("cursor left hidden: %q", got)
	}
	if strings.Index(got, "✗ stopped") > strings.Index(got, "brooom:") {
		t.Errorf("the error line must come after the summary: %q", got)
	}
}

func TestCommandsLeaveNoGoroutinesBehind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("goroutine accounting of the console reader differs on Windows")
	}
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	runTTY(t, true, nil, "sweep", "after-agents", "-d", "merged-branch", "--dry-run") // warm up shared, lazily started goroutines
	before := runtime.NumGoroutine()
	for range 3 {
		if code, _, errOut := runTTY(t, true, nil, "sweep", "after-agents", "-d", "merged-branch", "--dry-run"); code != ExitOK {
			t.Fatalf("code %d, stderr %q", code, errOut)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: %d before, %d after\n%s", before, runtime.NumGoroutine(), buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}
