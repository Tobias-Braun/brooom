package action

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// forgedUndoEntry builds an applied trash entry whose recorded original path
// is dest, the way an edited manifest would look.
func forgedUndoEntry(dest string) session.Entry {
	return session.Entry{
		Action: findings.ActionTrash, Status: session.StatusApplied, Path: dest,
		Trash: &trash.Record{OriginalPath: dest, StoredPath: dest + ".stored", Strategy: trash.StrategyTrash},
	}
}

func TestTrashUndoRefusesForgedDestinations(t *testing.T) {
	ctx := context.Background()
	fx := newTrashFixture(t)
	stub := &stubTrasher{}
	fx.useStub(stub)
	fx.mkdir("proj/.git/hooks")
	fx.mkdir("brooom-home/sessions")
	fx.mkdir("brooom-home/cache")

	tests := []struct{ name, dest string }{
		{"git hook", fx.path("proj/.git/hooks/pre-commit")},
		{"git dir itself", fx.path("proj/.git")},
		{"oddly cased git dir", fx.path("proj/.GIT/config")},
		{"session manifest", fx.path("brooom-home/sessions/20260101-000000-abcd.json")},
		{"brooom cache", fx.path("brooom-home/cache/file")},
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
	stub := &stubTrasher{}
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
	stub := &stubTrasher{}
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
		stub := &stubTrasher{}
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
		stub := &stubTrasher{}
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
}

// forgedRepoFixture is a repository with a tracked file, an ignored build
// directory and an untracked file, plus a fixture whose guard allows the
// repository's parent.
func forgedRepoFixture(t *testing.T, force bool) (*trashFixture, *testutil.Repo) {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("src/a.go", "package a\n")
	repo.WriteFile(".gitignore", "build/\n")
	repo.CommitAll("add src", testutil.BaseTime)
	repo.WriteFile("build/out.bin", "binary")
	repo.WriteFile("notes/todo.txt", "only copy")
	fx := newTrashFixture(t)
	guard, err := scope.NewGuard(filepath.Dir(repo.Dir))
	if err != nil {
		t.Fatal(err)
	}
	fx.env.Guard = guard
	fx.env.Force = force
	return fx, repo
}

// TestTrashApplyForgedStepEmptyMeta hands Apply steps that carry no Meta and
// no risk flags, so nothing the step says can vouch for its safety.
func TestTrashApplyForgedStepEmptyMeta(t *testing.T) {
	ctx := context.Background()
	t.Run("tracked files need force", func(t *testing.T) {
		fx, repo := forgedRepoFixture(t, false)
		stub := &stubTrasher{}
		fx.useStub(stub)
		_, err := trashAction{}.Apply(ctx, fx.env, Step{Finding: trashFinding(filepath.Join(repo.Dir, "src"))})
		wantSkip(t, err, "tracked by git")
		if len(stub.removed) != 0 {
			t.Fatal("tracked files were removed without --force")
		}
	})
	t.Run("tracked files with force", func(t *testing.T) {
		fx, repo := forgedRepoFixture(t, true)
		stub := &stubTrasher{}
		fx.useStub(stub)
		if _, err := (trashAction{}).Apply(ctx, fx.env, Step{Finding: trashFinding(filepath.Join(repo.Dir, "src"))}); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(stub.removed) != 1 {
			t.Fatalf("removed = %d, want 1", len(stub.removed))
		}
	})
}

func TestTrashUndoRefusesBrooomHome(t *testing.T) {
	fx := newTrashFixture(t)
	stub := &stubTrasher{}
	fx.useStub(stub)
	fx.mkdir("brooom-home")
	for _, rel := range []string{"brooom-home/config.toml", "brooom-home/cache/x"} {
		err := trashAction{}.Undo(context.Background(), fx.env, forgedUndoEntry(fx.path(rel)))
		if err == nil || !strings.Contains(err.Error(), "refusing to restore") {
			t.Fatalf("Undo(%s) err = %v, want a refusal", rel, err)
		}
	}
	if len(stub.restored) != 0 {
		t.Fatal("trasher was called")
	}
}

// TestBranchUndoRefusesRepositoryOutsideScope forges a manifest path that
// lies inside the allowed root but belongs to a repository whose top level
// is outside every allowed root. Git would write the ref into that outer
// repository, so the undo must refuse.
func TestBranchUndoRefusesRepositoryOutsideScope(t *testing.T) {
	repo := testutil.NewRepo(t)
	sub := filepath.Join(repo.Dir, "scan")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	guard, err := scope.NewGuard(sub)
	if err != nil {
		t.Fatal(err)
	}
	git, err := gitx.NewExecRunner()
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	env := &Env{Guard: guard, Git: git}
	e := session.Entry{Action: findings.ActionDeleteBranch, Path: sub,
		Undo: map[string]string{"branch": "planted", "sha": repo.Head()}}
	err = deleteBranch{}.Undo(context.Background(), env, e)
	if err == nil || !strings.Contains(err.Error(), "outside the allowed roots") {
		t.Fatalf("Undo err = %v, want a refusal", err)
	}
	if out := repo.Git("branch", "--list", "planted"); out != "" {
		t.Fatalf("branch was planted in the outer repository: %q", out)
	}
}

// TestBranchUndoRejectsMalformedData covers the static checks that already
// held before the repository-scope check was added.
func TestBranchUndoRejectsMalformedData(t *testing.T) {
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
