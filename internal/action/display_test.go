package action

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// TestDisplayCommandDialects pins the POSIX and PowerShell renderings.
func TestDisplayCommandDialects(t *testing.T) {
	tests := []struct {
		name, goos, path, want string
	}{
		{"posix", "darwin", "/p/dist", "trash /p/dist"},
		{"posix quote", "linux", "/p/a b", "trash '/p/a b'"},
		{"windows is labelled illustrative", "windows", `C:\p\dist`,
			`# illustrative, Brooom sends it to the Recycle Bin: Remove-Item -LiteralPath 'C:\p\dist' -Recurse`},
		{"windows quote", "windows", `C:\p\it's`,
			`# illustrative, Brooom sends it to the Recycle Bin: Remove-Item -LiteralPath 'C:\p\it''s' -Recurse`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayCommandFor(tt.goos, tt.path); got != tt.want {
				t.Errorf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDescribeCarriesNoSize: the size is printed once by the plan and the
// prompts, so a description with a size would show it twice.
func TestDescribeCarriesNoSize(t *testing.T) {
	if d := describe("/p/node_modules", nil); strings.ContainsAny(d, "()") {
		t.Errorf("describe = %q contains a size", d)
	}
}

// TestPlanPrintsSizeOnce: end to end through the trash action, the rendered
// plan and the individual prompt show the size of an item exactly once, and a
// step without a size (a branch) shows none.
func TestPlanPrintsSizeOnce(t *testing.T) {
	fx := newTrashFixture(t)
	p := fx.write("proj/build/out.bin", strings.Repeat("x", 8192))
	step, err := (trashAction{}).Plan(context.Background(), fx.env, trashFinding(filepath.Dir(p)))
	if err != nil {
		t.Fatal(err)
	}
	if step.Finding.SizeBytes == 0 {
		t.Fatal("fixture has no size")
	}
	line := itemLine(step)
	if strings.Count(line, "(") != 1 || !strings.HasPrefix(line, "move build to ") {
		t.Errorf("item line = %q, want one size", line)
	}
	var b strings.Builder
	renderPlan(&b, &Plan{Groups: []Group{{Detector: "d", Action: findings.ActionTrash, Items: []Item{{Step: step}}}}})
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.Contains(l, "move build") && strings.Count(l, "(") != 1 {
			t.Errorf("plan line %q shows the size %d times", l, strings.Count(l, "("))
		}
	}
}

func TestItemLineOmitsZeroSize(t *testing.T) {
	s := Step{Finding: findings.Finding{Kind: findings.KindBranch}, Description: "delete branch x with -d (fully merged into HEAD)"}
	if got := itemLine(s); got != s.Description || strings.Contains(got, "0 B") {
		t.Errorf("itemLine = %q", got)
	}
}

// displayVerb is the leading word of the display command on the host OS, so
// tests asserting on plan commands hold on every platform (the display
// follows the host shell: PowerShell on Windows, POSIX elsewhere).
func displayVerb() string {
	if runtime.GOOS == "windows" {
		return "# illustrative"
	}
	return "trash "
}
