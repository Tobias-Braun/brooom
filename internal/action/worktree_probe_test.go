package action

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// stubProbe enables the Windows-only probe on every OS and replaces the
// rename with fn for one test.
func stubProbe(t *testing.T, fn func(string) (error, error)) *[]string {
	t.Helper()
	oldOn, oldFn := probeDirHandles, probeWorktreeRename
	var calls []string
	probeDirHandles = true
	probeWorktreeRename = func(p string) (error, error) {
		calls = append(calls, p)
		return fn(p)
	}
	t.Cleanup(func() { probeDirHandles, probeWorktreeRename = oldOn, oldFn })
	return &calls
}

// TestRemoveWorktreeDeleteProbesDirectoryHandles covers issue #213: under the
// delete strategy on Windows a sharing violation on the probe rename refuses
// the removal, before git empties the directory, with or without --force.
func TestRemoveWorktreeDeleteProbesDirectoryHandles(t *testing.T) {
	sharing := errors.New("The process cannot access the file because it is being used by another process")
	for _, force := range []bool{false, true} {
		fx := newWTFixture(t)
		fx.strategy = config.StrategyDelete
		fx.env.Force = force
		path := fx.add("wt", "feat")
		calls := stubProbe(t, func(string) (error, error) { return sharing, nil })

		// Plan stays free of side effects: the probe renames, so a dry run
		// must not run it.
		if _, err := fx.plan(removeWorktree{}, fx.removeFinding(path)); err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if len(*calls) != 0 {
			t.Fatalf("Plan ran the rename probe: %v", *calls)
		}

		en, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
		if err != nil {
			t.Fatal(err)
		}
		if en.Status != session.StatusSkipped {
			t.Fatalf("force=%v: status = %q, want skipped (%+v)", force, en.Status, en)
		}
		wantContains(t, en.Error, "holds the worktree directory")
		if !exists(path) || !fx.registered(path) {
			t.Errorf("force=%v: worktree was touched", force)
		}
	}
}

func TestRemoveWorktreeProbePassesAndTrashSkipsIt(t *testing.T) {
	t.Run("delete with a free directory removes it", func(t *testing.T) {
		fx := newWTFixture(t)
		fx.strategy = config.StrategyDelete
		path := fx.add("wt", "feat")
		calls := stubProbe(t, func(string) (error, error) { return nil, nil })
		en, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
		if err != nil || en.Status != session.StatusApplied {
			t.Fatalf("entry = %+v, err = %v", en, err)
		}
		if len(*calls) != 1 {
			t.Errorf("probe calls = %v, want 1", *calls)
		}
	})
	t.Run("a probe that cannot restore fails loudly", func(t *testing.T) {
		fx := newWTFixture(t)
		fx.strategy = config.StrategyDelete
		path := fx.add("wt", "feat")
		stubProbe(t, func(p string) (error, error) { return nil, errProbeRestore{from: p, to: p + ".x"} })
		en, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
		var restore errProbeRestore
		if !errors.As(err, &restore) || en.Status == session.StatusApplied {
			t.Fatalf("entry = %+v, err = %v; want a failure naming the restore problem", en, err)
		}
	})
	t.Run("quarantine never probes", func(t *testing.T) {
		fx := newWTFixture(t)
		path := fx.add("wt", "feat")
		calls := stubProbe(t, func(string) (error, error) { return nil, nil })
		if _, err := fx.apply(removeWorktree{}, fx.removeFinding(path)); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 0 {
			t.Errorf("probe ran for the quarantine strategy: %v", *calls)
		}
	})
}

// TestRenameProbeRestoresDirectory: the real probe leaves the directory
// where it was, contents intact, and reports a missing directory as in use
// (the rename failed) instead of silently passing.
func TestRenameProbeRestoresDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	inUse, err := renameProbe(dir)
	if inUse != nil || err != nil {
		t.Fatalf("renameProbe = %v, %v", inUse, err)
	}
	if !exists(filepath.Join(dir, "sub")) {
		t.Error("directory not restored")
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	if len(entries) != 1 {
		t.Errorf("probe left extra entries: %v", entries)
	}
	if inUse, _ := renameProbe(filepath.Join(dir, "missing")); inUse == nil {
		t.Error("a failed rename must be reported as in use")
	}
}
