package buildartifacts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// TestIncompleteSizeIsFlagged builds the finding directly from a summary
// that walk marked Incomplete, so it runs on every OS and as root.
func TestIncompleteSizeIsFlagged(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/a/index.js")
	f.settle(f.daysAgo(100))
	base := f.byRel()["node_modules"]
	if base.SuggestedAction.Type != findings.ActionTrash || hasEvidence(base, "unreadable") {
		t.Fatalf("complete directory must stay actionable: %+v", base)
	}

	s := &scan{env: f.env(), target: f.target(), cfg: f.cfg}
	facts := facts{path: base.Path, sum: walk.DirSummary{SizeBytes: 1 << 20, Incomplete: true}}
	facts.c.m.rule = &rule{id: "node-modules", ecosystem: "node", description: "deps"}
	got := s.build(facts)
	if got.SuggestedAction.Type != findings.ActionNone || got.SuggestedAction.Reason != "cannot read part of the directory" {
		t.Errorf("action = %+v", got.SuggestedAction)
	}
	if !hasEvidence(got, "unreadable") {
		t.Errorf("evidence = %+v", got.Evidence)
	}
}

// TestUnreadableSubdirectoryEndToEnd uses a real chmod 000 directory below
// node_modules, as in the bug report: the finding stays (with a partial
// size) but suggests no action.
func TestUnreadableSubdirectoryEndToEnd(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read chmod 000 directories")
	}
	f := newFixture(t, false)
	f.write("package.json", "node_modules/ok/a.js", "node_modules/locked/b.js")
	f.settle(f.daysAgo(100))
	f.cfg.Thresholds.MinSizeBytes = 1
	locked := filepath.Join(f.dir, "node_modules", "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Skip("cannot deny access on this platform:", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("chmod does not deny access here")
	}
	got := f.byRel()["node_modules"]
	if got.ID == "" {
		t.Fatal("finding was dropped")
	}
	if got.SuggestedAction.Type != findings.ActionNone || !hasEvidence(got, "unreadable") {
		t.Errorf("finding = %+v", got)
	}
}
