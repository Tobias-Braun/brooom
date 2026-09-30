package action

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/session"
)

const (
	hostileRef  = "feat/\x1b[2Jevil\nFORGED"
	hostilePath = "/work/dir\x1b]0;pwn\x07/na\nme"
)

// assertClean fails when out holds a raw control character other than the
// newlines the renderers write themselves, or a line forged through a
// newline embedded in an untrusted value.
func assertClean(t *testing.T, out string) {
	t.Helper()
	for _, r := range strings.ReplaceAll(out, "\n", "") {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control rune %q in output:\n%q", r, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		for _, forged := range []string{"FORGED", "me", "fake"} {
			if line == forged || strings.HasPrefix(line, forged+" ") {
				t.Errorf("forged line %q in output:\n%s", line, out)
			}
		}
	}
}

func hostileFinding() findings.Finding {
	return findings.Finding{Detector: "merged-branch", Path: hostilePath, Ref: hostileRef,
		Kind: findings.KindBranch, SizeBytes: 5}
}

func TestRenderPlanSanitizes(t *testing.T) {
	f := hostileFinding()
	p := &Plan{
		Groups:  []Group{{Detector: "det\nector", Action: findings.ActionTrash, Items: []Item{{Step: Step{Finding: f, Description: "trash " + hostilePath}}}}},
		Skipped: []Skip{{Finding: f, Reason: "why\x1b[31m\nfake"}},
		Failed:  []Skip{{Finding: f, Reason: "boom\nfake"}},
	}
	var out bytes.Buffer
	renderPlan(&out, p)
	assertClean(t, out.String())
	if !strings.Contains(out.String(), `na\nme`) {
		t.Errorf("escaped path missing:\n%s", out.String())
	}
}

func TestPromptsSanitize(t *testing.T) {
	f := hostileFinding()
	p := &Plan{Groups: []Group{{Detector: "det\nector", Action: findings.ActionTrash,
		Items: []Item{{Step: Step{Finding: f, Description: "trash " + hostilePath}}}}}}
	var out bytes.Buffer
	newConfirmer(strings.NewReader("i\ny\n"), &out).confirm(p)
	assertClean(t, strings.ReplaceAll(out.String(), "\n", "\n"))
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("raw escape in prompt output:\n%q", out.String())
	}
}

func TestSummaryAndHintsSanitize(t *testing.T) {
	en := session.Entry{Path: hostilePath, Ref: hostileRef, RecoveryHint: "hint\nfake", Error: "err\nfake"}
	r := &Result{Applied: 1, Failed: 1, Entries: []session.Entry{en}, Failures: []session.Entry{en}, SessionID: "s1"}
	var out bytes.Buffer
	renderSummary(&out, r, []Skip{{Finding: hostileFinding(), Reason: "r\nfake"}}, false)
	assertClean(t, out.String())
}

func TestUndoRenderSanitizes(t *testing.T) {
	en := session.Entry{Path: hostilePath, Ref: hostileRef, RecoveryHint: "hint\nfake"}
	m := &session.Manifest{ID: "s1", Command: "cmd\nfake"}
	steps := []UndoStep{
		{Entry: en, Kind: UndoRestore, Description: "restore " + hostilePath},
		{Entry: en, Kind: UndoConflict, Reason: "c\nfake"},
		{Entry: en, Kind: UndoDone, Reason: "d\nfake"},
		{Entry: en, Kind: UndoCannot, Reason: "x\nfake"},
	}
	var out bytes.Buffer
	renderUndoPlan(&out, m, steps)
	renderUndoSummary(&out, &UndoResult{SessionID: "s1", Problems: []UndoProblem{{Label: hostilePath, Message: "m\nfake"}}})
	assertClean(t, out.String())
}
