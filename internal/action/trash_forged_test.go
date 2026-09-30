package action

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// forgedUndoEntry builds an applied trash entry whose recorded original path
// is dest, the way an edited manifest would look.
func forgedUndoEntry(dest string) session.Entry {
	return session.Entry{
		Action: findings.ActionTrash, Status: session.StatusApplied, Path: dest,
		Trash: &trash.Record{OriginalPath: dest, StoredPath: dest + ".stored", Strategy: config.StrategyQuarantine},
	}
}

func TestTrashUndoRefusesForgedDestinations(t *testing.T) {
	ctx := context.Background()
	fx := newTrashFixture(t)
	stub := &stubTrasher{strategy: config.StrategyQuarantine}
	fx.useStub(stub)
	fx.mkdir("proj/.git/hooks")
	fx.mkdir("brooom-home/sessions")
	fx.mkdir("brooom-home/quarantine/s1")

	tests := []struct{ name, dest string }{
		{"git hook", fx.path("proj/.git/hooks/pre-commit")},
		{"git dir itself", fx.path("proj/.git")},
		{"oddly cased git dir", fx.path("proj/.GIT/config")},
		{"session manifest", fx.path("brooom-home/sessions/20260101-000000-abcd.json")},
		{"quarantine content", fx.path("brooom-home/quarantine/s1/file")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := trashAction{}.Undo(ctx, fx.env, forgedUndoEntry(tc.dest))
			if err == nil || !strings.Contains(err.Error(), "refusing to restore") {
				t.Fatalf("Undo err = %v, want a refusal", err)
			}
			if len(stub.restored) != 0 {
				t.Fatalf("trasher was called for %s", tc.dest)
			}
		})
	}
}

// TestTrashUndoRefusesGitAliasByIdentity reaches .git through a symlinked
// spelling that no name check could see; only file identity catches it.
func TestTrashUndoRefusesGitAliasByIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	fx := newTrashFixture(t)
	stub := &stubTrasher{strategy: config.StrategyQuarantine}
	fx.useStub(stub)
	fx.mkdir("proj/.git")
	if err := os.Symlink(fx.path("proj/.git"), fx.path("proj/alias")); err != nil {
		t.Fatal(err)
	}
	err := trashAction{}.Undo(context.Background(), fx.env, forgedUndoEntry(fx.path("proj/alias/hooks")))
	if err == nil || len(stub.restored) != 0 {
		t.Fatalf("Undo err = %v, restored = %d, want a refusal", err, len(stub.restored))
	}
}

func TestTrashUndoStillRestoresOrdinaryDestination(t *testing.T) {
	fx := newTrashFixture(t)
	stub := &stubTrasher{strategy: config.StrategyQuarantine}
	fx.useStub(stub)
	fx.mkdir("proj")
	if err := (trashAction{}).Undo(context.Background(), fx.env, forgedUndoEntry(fx.path("proj/a.log"))); err != nil {
		t.Fatal(err)
	}
	if len(stub.restored) != 1 {
		t.Fatalf("restored = %d, want 1", len(stub.restored))
	}
}

// TestTrashApplyRechecksForgedStep hands Apply steps that skipped Plan.
func TestTrashApplyRechecksForgedStep(t *testing.T) {
	ctx := context.Background()

	t.Run("nested git repository", func(t *testing.T) {
		fx := newTrashFixture(t)
		stub := &stubTrasher{strategy: config.StrategyQuarantine}
		fx.useStub(stub)
		fx.mkdir("proj/vendor/dep/.git")
		fx.write("proj/vendor/dep/a.txt", "x")
		_, err := trashAction{}.Apply(ctx, fx.env, Step{Finding: trashFinding(fx.path("proj/vendor"))})
		wantSkip(t, err, "git repository")
		if len(stub.removed) != 0 {
			t.Fatal("directory with a nested repository was removed")
		}
	})
	t.Run("open file", func(t *testing.T) {
		fx := newTrashFixture(t)
		stub := &stubTrasher{strategy: config.StrategyQuarantine}
		fx.useStub(stub)
		p := fx.write("proj/a.log", "x")
		fx.setOpen(func(_ context.Context, paths []string) (map[string]bool, error) {
			return map[string]bool{paths[0]: true}, nil
		})
		_, err := trashAction{}.Apply(ctx, fx.env, Step{Finding: trashFinding(p)})
		wantSkip(t, err, "open by a process")
		if len(stub.removed) != 0 {
			t.Fatal("open file was removed")
		}
	})
	t.Run("delete strategy on untracked data", func(t *testing.T) {
		fx := newTrashFixture(t)
		fx.env.Force = true
		f := deleteUntracked(fx)
		stub := &stubTrasher{strategy: config.StrategyDelete}
		fx.useStub(stub)
		_, err := trashAction{}.Apply(ctx, fx.env, Step{Finding: f})
		wantSkip(t, err, "permanently delete untracked")
		if len(stub.removed) != 0 {
			t.Fatal("untracked file was deleted permanently")
		}
	})
}

func TestBranchUndoRejectsForgedData(t *testing.T) {
	fx := newTrashFixture(t)
	repoPath := fx.mkdir("proj")
	for _, tc := range []struct{ name, branch, sha string }{
		{"option-like branch", "--upload-pack=x", strings.Repeat("a", 40)},
		{"bad sha", "topic", "not-a-sha"},
		{"empty branch", "", strings.Repeat("a", 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := session.Entry{Action: findings.ActionDeleteBranch, Path: repoPath,
				Undo: map[string]string{"branch": tc.branch, "sha": tc.sha}}
			if err := (deleteBranch{}).Undo(context.Background(), fx.env, e); err == nil {
				t.Fatal("forged branch undo data accepted")
			}
		})
	}
}
