package gitx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestOperationInProgress(t *testing.T) {
	tests := []struct {
		marker string
		dir    bool
		op     string
	}{
		{"rebase-merge", true, "rebase"},
		{"rebase-apply", true, "rebase or am"},
		{"MERGE_HEAD", false, "merge"},
		{"CHERRY_PICK_HEAD", false, "cherry-pick"},
		{"REVERT_HEAD", false, "revert"},
		{"BISECT_LOG", false, "bisect"},
	}
	for _, tt := range tests {
		t.Run(tt.marker, func(t *testing.T) {
			dir := t.TempDir()
			if _, _, ok := OperationInProgress(dir); ok {
				t.Fatal("clean directory reported as busy")
			}
			p := filepath.Join(dir, tt.marker)
			var err error
			if tt.dir {
				err = os.Mkdir(p, 0o755)
			} else {
				err = os.WriteFile(p, nil, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			op, where, ok := OperationInProgress(t.TempDir(), dir)
			if !ok || op != tt.op || where != dir {
				t.Errorf("got %q %q %v", op, where, ok)
			}
		})
	}
}

func TestGitDirAndWorktreeGitDirs(t *testing.T) {
	r, err := NewExecRunner()
	if err != nil {
		t.Skip("git not installed")
	}
	repo := testutil.NewRepo(t)
	repo.Branch("side")
	wt := repo.AddWorktree("wt", "side")
	ctx := context.Background()

	main, err := GitDir(ctx, r, repo.Dir)
	if err != nil || main != filepath.Join(repo.Dir, ".git") {
		t.Fatalf("main git dir = %q, %v", main, err)
	}
	linked, err := GitDir(ctx, r, wt)
	if err != nil || filepath.Dir(linked) != filepath.Join(main, "worktrees") {
		t.Fatalf("linked git dir = %q, %v", linked, err)
	}
	dirs := WorktreeGitDirs(main)
	if len(dirs) != 2 || dirs[0] != main || dirs[1] != linked {
		t.Errorf("WorktreeGitDirs = %v", dirs)
	}
	if got := WorktreeGitDirs(filepath.Join(main, "missing")); len(got) != 1 {
		t.Errorf("missing common dir: %v", got)
	}
	if _, err := GitDir(ctx, r, t.TempDir()); err == nil {
		t.Error("GitDir outside a repository must fail")
	}
}
