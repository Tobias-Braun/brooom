package action

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// TestRenderPlanEscapesControlCharactersInCommand reproduces #182 item 9: a
// file name with an ESC survives shell quoting, so the displayed command has
// to escape it itself or the dry-run plan emits terminal sequences.
func TestRenderPlanEscapesControlCharactersInCommand(t *testing.T) {
	f := hostileFinding()
	cmd := displayCommandFor("linux", "/work/\x1b[2Jevil\nname")
	p := &Plan{Groups: []Group{{Detector: "d", Action: findings.ActionTrash,
		Items: []Item{{Step: Step{Finding: f, Description: "trash it", Command: cmd}}}}}}
	var out bytes.Buffer
	renderPlan(&out, p)
	assertClean(t, out.String())
	if !strings.Contains(out.String(), `\x1b`) {
		t.Errorf("escaped ESC missing from the command line:\n%s", out.String())
	}
}
