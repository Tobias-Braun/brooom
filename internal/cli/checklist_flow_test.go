package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/cli/checklist"
)

// TestSweepChooseUntickItems answers the sweep question with "e"; the list
// (a scripted stand-in for the terminal UI) unticks feat/merged, so only
// feat/squash is deleted and the summary counts one item.
func TestSweepChooseUntickItems(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	var shown []checklist.Item
	var out, errOut bytes.Buffer
	a := &app{
		io:       IO{In: strings.NewReader("e\n"), Out: &out, Err: &errOut},
		stdinTTY: func() bool { return true },
		choose: func(_ io.Reader, _ io.Writer, items []checklist.Item) ([]bool, bool, error) {
			shown = items
			checked := make([]bool, len(items))
			for i, it := range items {
				checked[i] = !strings.Contains(it.Label, "feat/merged")
			}
			return checked, true, nil
		},
	}
	code := execute(a, sweepArgs())
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut.String(), out.String())
	}
	if len(shown) != 2 || !shown[0].Checked || !strings.Contains(shown[0].Group, "merged-branch / delete-branch") {
		t.Errorf("list items %+v", shown)
	}
	if !f.hasBranch("feat/merged") || f.hasBranch("feat/squash") {
		t.Errorf("branches %v, want feat/merged kept and feat/squash deleted", f.branches())
	}
	for _, want := range []string{"[y/N/e to choose]", "cleaning 1 of 2 items", "1 item kept as you chose", "1 merged branch removed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// TestSweepChooseAbortChangesNothing: leaving the list without confirming is
// the same as answering no.
func TestSweepChooseAbortChangesNothing(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	var out bytes.Buffer
	a := &app{
		io:       IO{In: strings.NewReader("e\n"), Out: &out, Err: &bytes.Buffer{}},
		stdinTTY: func() bool { return true },
		choose: func(io.Reader, io.Writer, []checklist.Item) ([]bool, bool, error) {
			return nil, false, nil
		},
	}
	if code := execute(a, sweepArgs()); code != ExitOK {
		t.Fatalf("code %d\n%s", code, out.String())
	}
	if !f.hasBranch("feat/merged") || !f.hasBranch("feat/squash") || len(f.sessions()) != 0 {
		t.Errorf("an aborted list changed something: %v", f.branches())
	}
}

// TestNoListWithoutATerminal: without a terminal on stdout the question does
// not offer "e", since the list could not be shown.
func TestNoListWithoutATerminal(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, _ := runApp(t, "n\n", true, time.Time{}, sweepArgs()...)
	if code != ExitOK || strings.Contains(out, "e to choose") || !strings.Contains(out, "[y/N]") {
		t.Errorf("code %d\n%s", code, out)
	}
}
