package action

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// moveRestoredInto performs the prose step of the recovery hint: everything in
// restored except its .git file goes into path, which keeps the .git file that
// `git worktree add --no-checkout` created.
func moveRestoredInto(t *testing.T, restored, path string) {
	t.Helper()
	entries, err := os.ReadDir(restored)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.Rename(filepath.Join(restored, e.Name()), filepath.Join(path, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
}

// followTrashedHint plays the manual recovery of a trashed worktree exactly as
// the hint describes it: restore the trash copy to a temporary path (the
// step brooom undo would do), then run every hinted git command in order.
func followTrashedHint(t *testing.T, fx *wtFixture, en session.Entry, path string) {
	t.Helper()
	restored := filepath.Join(testutil.ResolvedTempDir(t), "restored")
	if err := os.Rename(en.Trash.StoredPath, restored); err != nil {
		t.Fatal(err)
	}
	for _, st := range trashedRecoverySteps(en.Undo) {
		if st.git == nil {
			moveRestoredInto(t, restored, path)
			continue
		}
		if _, err := fx.git.Run(context.Background(), st.dir, st.git...); err != nil {
			t.Fatalf("hinted command %v in %s failed: %v", st.git, st.dir, err)
		}
	}
}

// TestTrashedWorktreeManualRecoveryHint: the manual fallback for a trashed
// worktree must really work against git. The former hint, restore to the
// original path and run `git worktree add <path> <branch>`, fails with
// "already exists" because the restored directory holds a .git file.
func TestTrashedWorktreeManualRecoveryHint(t *testing.T) {
	for _, branch := range []string{"feat", ""} {
		name := "branch"
		if branch == "" {
			name = "detached"
		}
		t.Run(name, func(t *testing.T) {
			fx := newWTFixture(t)
			path := fx.add("wt", branch)
			testutil.WriteFile(t, path, "notes/todo.txt", "unsaved work\n")
			fx.env.Force = true

			en, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
			if err != nil {
				t.Fatal(err)
			}
			fx.wantTrashedEntry(en, path)
			wantContains(t, en.RecoveryHint, "brooom undo", "--no-checkout", "worktree repair", "reset", "<restored>")

			followTrashedHint(t, fx, en, path)

			if !fx.registered(path) {
				t.Fatal("not registered after the manual recovery")
			}
			got, err := os.ReadFile(filepath.Join(path, "notes", "todo.txt"))
			if err != nil || string(got) != "unsaved work\n" {
				t.Errorf("todo.txt = %q, %v", got, err)
			}
			wantContains(t, fx.gitOut(path, "status", "--porcelain"), "?? notes/")
			if b := fx.gitOut(path, "branch", "--show-current"); b != branch {
				t.Errorf("branch = %q, want %q", b, branch)
			}
		})
	}
}
