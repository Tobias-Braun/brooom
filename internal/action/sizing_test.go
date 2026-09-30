package action

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// buildSizedTree writes a node_modules-like tree: nested directories, small
// and larger files, a symlink and (where supported) a hard link.
func buildSizedTree(t *testing.T, fx *trashFixture) string {
	t.Helper()
	fx.write("proj/node_modules/a/index.js", strings.Repeat("a", 5000))
	fx.write("proj/node_modules/a/lib/b.js", "b")
	fx.write("proj/node_modules/c/c.js", strings.Repeat("c", 70000))
	fx.mkdir("proj/node_modules/empty")
	dir := fx.path("proj/node_modules")
	if err := os.Symlink(filepath.Join(dir, "a"), filepath.Join(dir, "link")); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	_ = os.Link(filepath.Join(dir, "c", "c.js"), filepath.Join(dir, "a", "c-hardlink.js"))
	return dir
}

// TestPlanAndReclaimedSizeAgree: the plan announced "move dist (53.2 kB)" but
// the manifest recorded a smaller, logical number ("reclaimed: 50.0 kB"). The
// plan, the entry and the summary must all carry the number the shared rule
// (walk.DirSize: allocated bytes, directory blocks, hard links once) gives.
func TestPlanAndReclaimedSizeAgree(t *testing.T) {
	for _, strategy := range []config.TrashStrategy{config.StrategyQuarantine, config.StrategyTrash} {
		t.Run(string(strategy), func(t *testing.T) {
			if strategy == config.StrategyTrash && runtime.GOOS != "linux" {
				t.Skip("the OS trash of this platform is not exercised in tests")
			}
			fx := newTrashFixture(t)
			fx.strategy = strategy
			dir := buildSizedTree(t, fx)
			want, err := walk.DirSize(context.Background(), dir, walk.Options{Fresh: true})
			if err != nil {
				t.Fatal(err)
			}

			step, err := (trashAction{}).Plan(context.Background(), fx.env, trashFinding(dir))
			if err != nil {
				t.Fatal(err)
			}
			if step.Finding.SizeBytes != want.SizeBytes {
				t.Fatalf("plan size = %d, want %d", step.Finding.SizeBytes, want.SizeBytes)
			}
			en, err := (trashAction{}).Apply(context.Background(), fx.env, step)
			if err != nil {
				t.Fatal(err)
			}
			if en.Status != session.StatusApplied || en.SizeBytes != step.Finding.SizeBytes {
				t.Errorf("entry size = %d (status %s), want the planned %d", en.SizeBytes, en.Status, step.Finding.SizeBytes)
			}
		})
	}
}

// TestSizeMeasuredInPlanIsPassedToTrasher: Apply hands the size of its
// re-validating walk to the trasher, which must report exactly that instead
// of walking again.
func TestSizeMeasuredInPlanIsPassedToTrasher(t *testing.T) {
	fx := newTrashFixture(t)
	dir := buildSizedTree(t, fx)
	want, err := walk.DirSize(context.Background(), dir, walk.Options{Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubTrasher{}
	fx.useStub(stub)
	step, err := (trashAction{}).Plan(context.Background(), fx.env, trashFinding(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (trashAction{}).Apply(context.Background(), fx.env, step); err != nil {
		t.Fatal(err)
	}
	if len(stub.removeCtx) != 1 {
		t.Fatalf("Remove called %d times", len(stub.removeCtx))
	}
	if got, ok := trash.SizeHintFrom(stub.removeCtx[0], dir); !ok || got != want.SizeBytes {
		t.Errorf("size hint = %d, %v; want %d", got, ok, want.SizeBytes)
	}
}
