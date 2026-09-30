package action

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// TestRemoveWorktreeUndoRefusesForgedTargets: a manifest edited to point the
// restore into git metadata or Brooom's own state must be refused before git
// or the trasher writes anything, with and without a trash record (the record
// path is forged to match, so it cannot be what stops the undo).
func TestRemoveWorktreeUndoRefusesForgedTargets(t *testing.T) {
	fx := newWTFixture(t)
	path := fx.add("wt", "feat")
	applied, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirs.Sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	fx.roots = append(fx.roots, dirs.Sessions)
	fx.rebuildGuard()

	targets := map[string]string{
		"inside .git":        filepath.Join(fx.repo.Dir, ".git", "evilwt"),
		"hooks directory":    filepath.Join(fx.repo.Dir, ".git", "hooks"),
		"Brooom state dir":   filepath.Join(dirs.Sessions, "evilwt"),
		"the sessions dir":   dirs.Sessions,
		"oddly cased .GIT":   filepath.Join(fx.repo.Dir, ".GIT", "evilwt"),
		"inside .git/hooks":  filepath.Join(fx.repo.Dir, ".git", "hooks", "wt"),
		"main repo internal": filepath.Join(fx.repo.Dir, ".git", "worktrees", "evil"),
	}
	for name, target := range targets {
		for _, withTrash := range []bool{false, true} {
			label := name + " without trash"
			if withTrash {
				label = name + " with trash"
			}
			t.Run(label, func(t *testing.T) {
				en := applied
				en.Undo = map[string]string{"repo": fx.repo.Dir, "worktree": target, "branch": "feat", "head": applied.Undo["head"]}
				en.Trash = nil
				if withTrash {
					en.Trash = &trash.Record{OriginalPath: target, StoredPath: target + ".stored", Strategy: config.StrategyQuarantine, Restorable: true}
					en.Status = session.StatusApplied
				}
				err := removeWorktree{}.Undo(context.Background(), fx.env, en)
				if err == nil || !strings.Contains(err.Error(), "refusing to restore") {
					t.Fatalf("Undo err = %v, want a refusal", err)
				}
				if strings.Contains(name, "evil") || name == "inside .git" {
					if _, err := os.Lstat(target); err == nil {
						t.Errorf("%s was created", target)
					}
				}
				if fx.registered(target) {
					t.Errorf("%s was registered as a worktree", target)
				}
			})
		}
	}
}

// TestRemoveWorktreeUndoRequiresMainWorktree: the recorded repository must be
// the main worktree; a linked worktree (or any other directory of the
// repository) is not accepted as the place to run git worktree add from.
func TestRemoveWorktreeUndoRequiresMainWorktree(t *testing.T) {
	fx := newWTFixture(t)
	path := fx.add("wt", "feat")
	other := fx.add("other", "feat-other")
	en, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
	if err != nil {
		t.Fatal(err)
	}
	en.Undo["repo"] = other
	err = removeWorktree{}.Undo(context.Background(), fx.env, en)
	if err == nil || !strings.Contains(err.Error(), "main worktree") {
		t.Fatalf("Undo err = %v, want a main-worktree refusal", err)
	}
	if fx.registered(path) {
		t.Error("worktree was re-added from a linked worktree")
	}
	en.Undo["repo"] = fx.repo.Dir
	if err := (removeWorktree{}).Undo(context.Background(), fx.env, en); err != nil {
		t.Fatalf("Undo with the main worktree: %v", err)
	}
}

// failStat replaces the stat seam so that path (and only it) fails with err.
func failStat(t *testing.T, path string, err error) {
	t.Helper()
	old := stat
	stat = func(p string) (os.FileInfo, error) {
		if p == path {
			return nil, err
		}
		return old(p)
	}
	t.Cleanup(func() { stat = old })
}

// TestRestoreTargetFailsClosedOnStatErrors: an ancestor or sibling that cannot
// be inspected leaves its identity unknown, and unknown refuses.
func TestRestoreTargetFailsClosedOnStatErrors(t *testing.T) {
	fx := newTrashFixture(t)
	fx.mkdir("proj/sub")
	denied := &fs.PathError{Op: "stat", Err: fs.ErrPermission}
	tests := []struct{ name, failing string }{
		{"ancestor", fx.path("proj/sub")},
		{"sibling git entry", fx.path("proj/.git")},
		{"sibling mercurial entry", fx.path("proj/.hg")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failStat(t, tc.failing, denied)
			err := refuseRestoreTarget(fx.path("proj/sub/file"))
			if err == nil || !strings.Contains(err.Error(), "cannot inspect") || !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("err = %v, want a fail-closed refusal naming the cause", err)
			}
		})
	}
	t.Run("missing entries stay allowed", func(t *testing.T) {
		if err := refuseRestoreTarget(fx.path("proj/sub/new/file")); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestRestoreTargetRefusesEveryVCSAlias: a symlink spelling of .hg, .jj or
// .svn is caught by identity just like one of .git.
func TestRestoreTargetRefusesEveryVCSAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	for _, name := range []string{".git", ".hg", ".jj", ".svn"} {
		t.Run(name, func(t *testing.T) {
			fx := newTrashFixture(t)
			fx.mkdir("proj/" + name)
			if err := os.Symlink(fx.path("proj/"+name), fx.path("proj/alias")); err != nil {
				t.Fatal(err)
			}
			err := refuseRestoreTarget(fx.path("proj/alias/hooks"))
			if err == nil || !strings.Contains(err.Error(), "alias") {
				t.Fatalf("err = %v, want an alias refusal", err)
			}
		})
	}
}

// TestRefuseByIdentityCoversEveryVCSAlias covers the removal side of the
// alias check, and that an unreadable entry is reported as such instead of as
// an alias.
func TestRefuseByIdentityCoversEveryVCSAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	fx := newTrashFixture(t)
	for _, name := range []string{".hg", ".jj", ".svn"} {
		t.Run(name, func(t *testing.T) {
			fx.mkdir("proj/" + name)
			alias := fx.path("proj/alias-" + strings.TrimPrefix(name, "."))
			if err := os.Symlink(fx.path("proj/"+name), alias); err != nil {
				t.Fatal(err)
			}
			// A symlink is a different object under Lstat, so the alias
			// here must be a second name for the same directory: use a
			// hard-link-free spelling by pointing the identity seam.
			old := isSameEntry
			isSameEntry = func(a, b string) bool { return a == alias && b == fx.path("proj/"+name) }
			t.Cleanup(func() { isSameEntry = old })
			err := RefuseByIdentity(filepath.Join(alias, "x"))
			if err == nil || !strings.Contains(err.Error(), "alias of it") || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, want an alias of %s", err, name)
			}
		})
	}
	t.Run("stat error is reported as such", func(t *testing.T) {
		target := fx.path("proj/unreadable")
		fx.mkdir("proj/unreadable")
		old := lstat
		lstat = func(p string) (os.FileInfo, error) {
			if p == target {
				return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrPermission}
			}
			return old(p)
		}
		t.Cleanup(func() { lstat = old })
		err := RefuseByIdentity(target)
		if err == nil || strings.Contains(err.Error(), "alias of it") || !strings.Contains(err.Error(), "cannot tell") {
			t.Fatalf("err = %v, want a cannot-tell refusal that does not claim an alias", err)
		}
	})
}

// TestClassificationStartDirIgnoresLinks: a symlink to a directory is not a
// directory for trash classification, so its repository lookup starts at the
// parent (the same rule walk.IsDirNoFollow applies to Windows junctions).
func TestClassificationStartDirIgnoresLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	fx := newTrashFixture(t)
	fx.mkdir("real")
	link := fx.path("link")
	if err := os.Symlink(fx.path("real"), link); err != nil {
		t.Fatal(err)
	}
	if got := repoLookupStart(link); got != filepath.Dir(link) {
		t.Errorf("start = %s, want %s", got, filepath.Dir(link))
	}
	if got := repoLookupStart(fx.path("real")); got != fx.path("real") {
		t.Errorf("start = %s, want the directory itself", got)
	}
}

// TestPruneWorktreesFallsBackToTargetedDeregistration: when git refuses to
// drop the registration of a missing directory, prune-worktrees uses the same
// verified fallback as remove-worktree, removes only that entry and refuses a
// lock taken meanwhile.
func TestPruneWorktreesFallsBackToTargetedDeregistration(t *testing.T) {
	t.Run("targeted deregistration", func(t *testing.T) {
		fx := newWTFixture(t)
		keep := fx.add("keep", "keep")
		f := fx.missingWorktree("gone", "gone")
		step, err := fx.plan(pruneWorktrees{}, f)
		if err != nil {
			t.Fatal(err)
		}
		fx.env.Git = failingRunner{Runner: fx.git, fail: "remove"}
		en, err := pruneWorktrees{}.Apply(context.Background(), fx.env, step)
		if err != nil || en.Status != session.StatusApplied {
			t.Fatalf("entry = %+v, err = %v", en, err)
		}
		fx.env.Git = fx.git
		if fx.registered(f.Path) || !fx.registered(keep) {
			t.Errorf("registered gone=%v keep=%v, want only keep", fx.registered(f.Path), fx.registered(keep))
		}
	})
	t.Run("locked registration is kept", func(t *testing.T) {
		fx := newWTFixture(t)
		f := fx.missingWorktree("gone", "gone")
		step, err := fx.plan(pruneWorktrees{}, f)
		if err != nil {
			t.Fatal(err)
		}
		repo, err := gitx.Open(context.Background(), fx.git, fx.repo.Dir)
		if err != nil {
			t.Fatal(err)
		}
		admin, ok := gitx.WorktreeAdminDir(repo.Common, f.Path)
		if !ok {
			t.Fatal("no admin dir")
		}
		fx.env.Git = lockingRunner{Runner: fx.git, admin: admin}
		en, err := pruneWorktrees{}.Apply(context.Background(), fx.env, step)
		if err == nil || en.Status != session.StatusFailed {
			t.Fatalf("entry = %+v, err = %v, want a failure", en, err)
		}
		if _, err := os.Stat(admin); err != nil {
			t.Errorf("admin dir must survive: %v", err)
		}
	})
}
