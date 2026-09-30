package action

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// TestDisplayCommandDialects pins the POSIX and PowerShell renderings and the
// real quarantine destination (session directory plus numbered directory).
func TestDisplayCommandDialects(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		strategy config.TrashStrategy
		path     string
		qdir     string
		want     string
	}{
		{"posix delete", "linux", config.StrategyDelete, "/p/a b", "/h/quarantine", "rm -rf -- '/p/a b'"},
		{"posix trash", "darwin", config.StrategyTrash, "/p/dist", "/h/quarantine", "trash /p/dist"},
		{"posix quarantine", "linux", config.StrategyQuarantine, "/p/dist", "/h/quarantine",
			"mv -- /p/dist '/h/quarantine/<session-id>/<n>/'"},
		{"windows delete", "windows", config.StrategyDelete, `C:\p\a b`, `C:\h\quarantine`,
			`Remove-Item -LiteralPath 'C:\p\a b' -Recurse -Force`},
		{"windows quote", "windows", config.StrategyDelete, `C:\p\it's`, `C:\h\quarantine`,
			`Remove-Item -LiteralPath 'C:\p\it''s' -Recurse -Force`},
		{"windows quarantine", "windows", config.StrategyQuarantine, `C:\p\dist`, `C:\h\quarantine`,
			`Move-Item -LiteralPath 'C:\p\dist' -Destination 'C:\h\quarantine\<session-id>\<n>\'`},
		{"windows trash is labelled illustrative", "windows", config.StrategyTrash, `C:\p\dist`, `C:\h\quarantine`,
			`# illustrative, Brooom sends it to the Recycle Bin: Remove-Item -LiteralPath 'C:\p\dist' -Recurse`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayCommandFor(tt.goos, tt.strategy, tt.path, tt.qdir); got != tt.want {
				t.Errorf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDisplayCommandQuarantineNamesRealDestination: the old plan printed
// `mv <path> <home>/quarantine`, but the item lands in
// quarantine/<session>/<n>/.
func TestDisplayCommandQuarantineNamesRealDestination(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h")
	t.Setenv(config.HomeEnv, home)
	got := displayCommandFor("linux", config.StrategyQuarantine, "/p/x", quarantineDir())
	if !strings.Contains(got, "<session-id>") || !strings.Contains(got, "<n>") {
		t.Errorf("command %q does not show the session destination", got)
	}
}

// TestDescribeCarriesNoSize: the size is printed once by the plan and the
// prompts, so a description with a size would show it twice.
func TestDescribeCarriesNoSize(t *testing.T) {
	for _, s := range []config.TrashStrategy{config.StrategyTrash, config.StrategyQuarantine, config.StrategyDelete} {
		if d := describe(s, "/p/node_modules", nil); strings.ContainsAny(d, "()") {
			t.Errorf("describe(%s) = %q contains a size", s, d)
		}
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
