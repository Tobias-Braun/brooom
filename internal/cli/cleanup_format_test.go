package cli

import (
	"strings"
	"testing"
)

// TestShortcutDryRunHonoursFormat reproduces #111: every human format
// printed the same executor plan.
func TestShortcutDryRunHonoursFormat(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	outs := map[string]string{}
	for _, format := range []string{"table", "tree", "summary"} {
		code, out, errOut := brooom(t, "", "sweep", "after-agents", "-d", "merged-branch", "--dry-run", "-f", format)
		if code != ExitOK {
			t.Fatalf("%s: code %d, stderr %q", format, code, errOut)
		}
		if !strings.Contains(out, "dry run: nothing was changed") {
			t.Errorf("%s: plan hint missing:\n%s", format, out)
		}
		outs[format] = out
	}
	if outs["table"] == outs["tree"] || outs["table"] == outs["summary"] || outs["tree"] == outs["summary"] {
		t.Errorf("formats print identical output: %v", outs)
	}
	if !strings.Contains(outs["summary"], "DETECTOR") {
		t.Errorf("summary format not rendered:\n%s", outs["summary"])
	}
}

// TestShortcutQuietIsTerse checks that --quiet drops the plan detail, the
// totals and the hint of a dry run and shrinks the apply summary.
func TestShortcutQuietIsTerse(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	_, loud, _ := brooom(t, "", "sweep", "after-agents", "-d", "merged-branch", "--dry-run")
	code, quiet, errOut := brooom(t, "", "sweep", "after-agents", "-d", "merged-branch", "--dry-run", "-q")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if len(quiet) >= len(loud) {
		t.Errorf("quiet is not shorter:\nquiet %q\nloud %q", quiet, loud)
	}
	for _, unwanted := range []string{"dry run", "total reclaimable", "re-run"} {
		if strings.Contains(quiet, unwanted) {
			t.Errorf("quiet output contains %q:\n%s", unwanted, quiet)
		}
	}

	_, loudApply, _ := brooom(t, "", "sweep", "after-agents", "-d", "merged-branch", "--yes")
	f2 := newCleanupFixture(t, nil)
	f2.mergedAndSquashed()
	_, quietApply, _ := brooom(t, "", "sweep", "after-agents", "-d", "merged-branch", "--yes", "-q")
	// A successful quiet sweep says nothing; the loud one ends with its
	// one-line summary.
	if quietApply != "" || !strings.Contains(loudApply, "2 merged branches removed") {
		t.Errorf("quiet apply:\nquiet %q\nloud %q", quietApply, loudApply)
	}
}
