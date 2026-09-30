package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// wtFixture is a real repository with linked worktrees, a real guard that
// covers the repository and the worktree directory, the real git runner and
// a quarantine trasher in a temp dir.
type wtFixture struct {
	t        *testing.T
	repo     *testutil.Repo
	env      *Env
	git      *gitx.ExecRunner
	quar     string
	strategy config.TrashStrategy
	roots    []string
}

func newWTFixture(t *testing.T) *wtFixture {
	t.Helper()
	git, err := gitx.NewExecRunner()
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	home := testutil.ResolvedTempDir(t)
	t.Setenv(config.HomeEnv, filepath.Join(home, "brooom"))
	t.Setenv("HOME", filepath.Join(home, "user"))
	t.Setenv("USERPROFILE", filepath.Join(home, "user"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	repo := testutil.NewRepo(t)
	fx := &wtFixture{
		t: t, repo: repo, git: git, quar: filepath.Join(home, "quarantine"),
		strategy: config.StrategyQuarantine, roots: []string{repo.Dir},
	}
	fx.env = &Env{
		Git: git,
		Trasher: func(string) (trash.Trasher, error) {
			return fx.trasher(fx.strategy)
		},
		TrasherFor: fx.trasher,
	}
	fx.rebuildGuard()
	return fx
}

func (fx *wtFixture) rebuildGuard() {
	fx.t.Helper()
	g, err := scope.NewGuard(fx.roots...)
	if err != nil {
		fx.t.Fatal(err)
	}
	fx.env.Guard = g
}

func (fx *wtFixture) trasher(s config.TrashStrategy) (trash.Trasher, error) {
	return trash.New(s, trash.Options{SessionID: "20260930-120000-wt", QuarantineDir: fx.quar})
}

// add creates a linked worktree and allows its parent directory in the guard.
func (fx *wtFixture) add(rel, branch string) string {
	fx.t.Helper()
	p := fx.repo.AddWorktree(rel, branch)
	if parent := filepath.Dir(p); !containsString(fx.roots, parent) {
		fx.roots = append(fx.roots, parent)
		fx.rebuildGuard()
	}
	return p
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// finding builds the finding the worktrees detector would emit for path.
func (fx *wtFixture) finding(path string, action findings.ActionType) findings.Finding {
	fx.t.Helper()
	repo, err := gitx.Open(context.Background(), fx.git, fx.repo.Dir)
	if err != nil {
		fx.t.Fatal(err)
	}
	list, err := repo.ListWorktrees(context.Background())
	if err != nil {
		fx.t.Fatal(err)
	}
	wt, ok := findWorktree(list, path)
	if !ok {
		fx.t.Fatalf("%s is not a worktree", path)
	}
	return findings.Finding{
		ID: findings.NewID("worktrees", findings.KindWorktree, path, wt.Branch), Detector: "worktrees",
		Path: path, Ref: wt.Branch, Kind: findings.KindWorktree, SizeBytes: 100,
		SuggestedAction: findings.SuggestedAction{Type: action},
		Meta:            map[string]string{"repo": fx.repo.Dir, "head": wt.Head, "branch": wt.Branch},
	}
}

func (fx *wtFixture) removeFinding(path string) findings.Finding {
	return fx.finding(path, findings.ActionRemoveWorktree)
}

func (fx *wtFixture) plan(a Action, f findings.Finding) (Step, error) {
	return a.Plan(context.Background(), fx.env, f)
}

// apply plans and applies, failing the test when planning fails.
func (fx *wtFixture) apply(a Action, f findings.Finding) (session.Entry, error) {
	fx.t.Helper()
	step, err := fx.plan(a, f)
	if err != nil {
		fx.t.Fatalf("Plan: %v", err)
	}
	return a.Apply(context.Background(), fx.env, step)
}

func (fx *wtFixture) gitOut(dir string, args ...string) string {
	fx.t.Helper()
	out, err := fx.git.Run(context.Background(), dir, args...)
	if err != nil {
		fx.t.Fatal(err)
	}
	return out
}

func (fx *wtFixture) registered(path string) bool {
	fx.t.Helper()
	repo, err := gitx.Open(context.Background(), fx.git, fx.repo.Dir)
	if err != nil {
		fx.t.Fatal(err)
	}
	list, err := repo.ListWorktrees(context.Background())
	if err != nil {
		fx.t.Fatal(err)
	}
	_, ok := findWorktree(list, path)
	return ok
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// wantMap asserts got holds every key of want with the same value.
func wantMap(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("[%s] = %q, want %q", k, got[k], v)
		}
	}
}

// wantContains asserts s contains every fragment.
func wantContains(t *testing.T, s string, fragments ...string) {
	t.Helper()
	for _, frag := range fragments {
		if !strings.Contains(s, frag) {
			t.Errorf("%q lacks %q", s, frag)
		}
	}
}

func TestWorktreeActionsRegistered(t *testing.T) {
	for _, ty := range []findings.ActionType{findings.ActionRemoveWorktree, findings.ActionPruneWorktrees} {
		if a, ok := Get(ty); !ok || a.Type() != ty {
			t.Errorf("action %s not registered", ty)
		}
	}
}

func TestRemoveWorktreeClean(t *testing.T) {
	fx := newWTFixture(t)
	path := fx.add("feat", "feat")
	f := fx.removeFinding(path)
	act := removeWorktree{}

	step, err := fx.plan(act, f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(step.Command, "git worktree remove --") || strings.Contains(step.Command, "--force") {
		t.Errorf("command = %q", step.Command)
	}
	en, err := act.Apply(context.Background(), fx.env, step)
	if err != nil {
		t.Fatal(err)
	}
	want := session.Entry{Status: session.StatusApplied, Restorable: true, Path: path, Ref: "feat", SizeBytes: 100}
	if en.Status != want.Status || en.Restorable != want.Restorable || en.Trash != nil ||
		en.Path != want.Path || en.Ref != want.Ref || en.SizeBytes != want.SizeBytes {
		t.Errorf("entry = %+v", en)
	}
	wantMap(t, en.Undo, map[string]string{"repo": fx.repo.Dir, "worktree": path, "branch": "feat", "head": f.Meta["head"]})
	wantContains(t, en.RecoveryHint, "git worktree add", "feat", "ignored by git")
	if exists(path) || fx.registered(path) {
		t.Error("worktree still present")
	}
	fx.gitOut(fx.repo.Dir, "branch", "-d", "feat")
}

func TestRemoveWorktreeUndoClean(t *testing.T) {
	tests := []struct {
		name         string
		branch       string
		prepare      func(fx *wtFixture, path string)
		wantBranch   string
		wantDetached bool
	}{
		{name: "branch form", branch: "feat", wantBranch: "feat"},
		{name: "detached worktree", branch: "", wantDetached: true},
		{
			name: "branch checked out elsewhere falls back to detached", branch: "feat", wantDetached: true,
			prepare: func(fx *wtFixture, _ string) { fx.repo.Checkout("feat") },
		},
		{
			name: "branch deleted falls back to detached", branch: "feat", wantDetached: true,
			prepare: func(fx *wtFixture, _ string) { fx.gitOut(fx.repo.Dir, "branch", "-D", "feat") },
		},
		{
			name: "empty target directory is accepted", branch: "feat", wantBranch: "feat",
			prepare: func(_ *wtFixture, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWTFixture(t)
			path := fx.add("wt", tc.branch)
			f := fx.removeFinding(path)
			en, err := fx.apply(removeWorktree{}, f)
			if err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil {
				tc.prepare(fx, path)
			}
			if err := (removeWorktree{}).Undo(context.Background(), fx.env, en); err != nil {
				t.Fatalf("Undo: %v", err)
			}
			if !fx.registered(path) {
				t.Fatal("worktree not registered after undo")
			}
			if head := fx.gitOut(path, "rev-parse", "HEAD"); head != f.Meta["head"] {
				t.Errorf("HEAD = %s, want %s", head, f.Meta["head"])
			}
			cur := fx.gitOut(path, "branch", "--show-current")
			if tc.wantDetached && cur != "" {
				t.Errorf("expected detached HEAD, on branch %q", cur)
			}
			if !tc.wantDetached && cur != tc.wantBranch {
				t.Errorf("branch = %q, want %q", cur, tc.wantBranch)
			}
		})
	}
}

func TestRemoveWorktreeUndoRefusals(t *testing.T) {
	fx := newWTFixture(t)
	path := fx.add("wt", "feat")
	en, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
	if err != nil {
		t.Fatal(err)
	}
	act := removeWorktree{}
	ctx := context.Background()

	t.Run("occupied path", func(t *testing.T) {
		testutil.WriteFile(t, path, "other.txt", "x")
		err := act.Undo(ctx, fx.env, en)
		if err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("err = %v", err)
		}
		if fx.registered(path) {
			t.Error("worktree was added despite occupied path")
		}
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("commit gone and branch missing", func(t *testing.T) {
		bad := en
		bad.Undo = map[string]string{
			"repo": fx.repo.Dir, "worktree": path, "branch": "no-such-branch",
			"head": strings.Repeat("ab", 20),
		}
		err := act.Undo(ctx, fx.env, bad)
		if err == nil || !strings.Contains(err.Error(), "no longer exists") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("outside the guard", func(t *testing.T) {
		bad := en
		bad.Undo = map[string]string{
			"repo": fx.repo.Dir, "worktree": filepath.Join(testutil.ResolvedTempDir(t), "wt"),
			"branch": "feat", "head": en.Undo["head"],
		}
		if err := act.Undo(ctx, fx.env, bad); err == nil {
			t.Fatal("expected refusal outside the guard")
		}
	})
	t.Run("not applied", func(t *testing.T) {
		bad := en
		bad.Status = session.StatusRestored
		if err := act.Undo(ctx, fx.env, bad); err == nil {
			t.Fatal("expected refusal")
		}
	})
	t.Run("foreign action", func(t *testing.T) {
		bad := en
		bad.Action = findings.ActionPruneWorktrees
		err := act.Undo(ctx, fx.env, bad)
		if err == nil || !strings.Contains(err.Error(), "not remove-worktree") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing data and relative paths", func(t *testing.T) {
		for _, u := range []map[string]string{nil, {"repo": "r", "worktree": "w", "head": "h"}} {
			bad := en
			bad.Undo = u
			if err := act.Undo(ctx, fx.env, bad); err == nil {
				t.Errorf("undo data %v accepted", u)
			}
		}
	})
}

// wantTrashedEntry asserts the entry of a dirty worktree moved to quarantine.
func (fx *wtFixture) wantTrashedEntry(en session.Entry, path string) {
	fx.t.Helper()
	if en.Status != session.StatusApplied || en.Trash == nil || !en.Restorable {
		fx.t.Fatalf("entry = %+v", en)
	}
	if en.Trash.Strategy != config.StrategyQuarantine {
		fx.t.Errorf("strategy = %s", en.Trash.Strategy)
	}
	wantContains(fx.t, en.RecoveryHint, "unstaged", "git worktree add")
	if exists(path) || fx.registered(path) {
		fx.t.Fatal("worktree still present")
	}
}

// wantRestoredDirty asserts that undo brought the dirty worktree of
// TestRemoveWorktreeDirty back: registered, files intact, changes unstaged.
func (fx *wtFixture) wantRestoredDirty(path string) {
	fx.t.Helper()
	if !fx.registered(path) {
		fx.t.Fatal("not registered after undo")
	}
	for rel, want := range map[string]string{"README.md": "# changed\n", "notes/todo.txt": "unsaved work\n"} {
		got, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			fx.t.Errorf("%s = %q, %v", rel, got, err)
		}
	}
	wantContains(fx.t, fx.gitOut(path, "status", "--porcelain"), " M README.md", "?? notes/")
	if b := fx.gitOut(path, "branch", "--show-current"); b != "feat" {
		fx.t.Errorf("branch = %q", b)
	}
}

func TestRemoveWorktreeDirty(t *testing.T) {
	setup := func(t *testing.T) (*wtFixture, string) {
		fx := newWTFixture(t)
		path := fx.add("feat", "feat")
		testutil.WriteFile(t, path, "README.md", "# changed\n")
		testutil.WriteFile(t, path, "notes/todo.txt", "unsaved work\n")
		fx.gitOut(path, "add", "README.md")
		return fx, path
	}

	t.Run("refused without force", func(t *testing.T) {
		fx, path := setup(t)
		_, err := fx.plan(removeWorktree{}, fx.removeFinding(path))
		wantSkip(t, err, "worktree has uncommitted changes; re-run with --force to trash it")
	})

	t.Run("delete strategy refused even with force", func(t *testing.T) {
		fx, path := setup(t)
		fx.env.Force = true
		fx.strategy = config.StrategyDelete
		_, err := fx.plan(removeWorktree{}, fx.removeFinding(path))
		wantSkip(t, err, "refusing to permanently delete uncommitted work; use --trash-strategy trash or quarantine")
		if !exists(path) {
			t.Fatal("worktree was touched")
		}
	})

	t.Run("no trasher with force", func(t *testing.T) {
		fx, path := setup(t)
		fx.env.Force = true
		fx.env.Trasher = func(string) (trash.Trasher, error) { return nil, errors.New("boom") }
		if _, err := fx.plan(removeWorktree{}, fx.removeFinding(path)); err == nil || errors.Is(err, ErrSkipped) {
			t.Fatalf("err = %v, want a hard failure", err)
		}
	})

	t.Run("force trashes, prunes and undo restores", func(t *testing.T) {
		fx, path := setup(t)
		fx.env.Force = true
		act := removeWorktree{}
		en, err := fx.apply(act, fx.removeFinding(path))
		if err != nil {
			t.Fatal(err)
		}
		fx.wantTrashedEntry(en, path)
		fx.gitOut(fx.repo.Dir, "branch", "-d", "feat") // the branch is free again
		fx.gitOut(fx.repo.Dir, "branch", "feat")

		if err := act.Undo(context.Background(), fx.env, en); err != nil {
			t.Fatalf("Undo: %v", err)
		}
		fx.wantRestoredDirty(path)
	})

	t.Run("prune failure keeps the trash record", func(t *testing.T) {
		fx, path := setup(t)
		fx.env.Force = true
		fx.env.Git = failingRunner{Runner: fx.git, fail: "prune"}
		step, err := fx.plan(removeWorktree{}, fx.removeFinding(path))
		if err != nil {
			t.Fatal(err)
		}
		en, err := removeWorktree{}.Apply(context.Background(), fx.env, step)
		if err == nil || en.Status != session.StatusFailed || en.Trash == nil || !en.Restorable {
			t.Fatalf("entry = %+v, err = %v", en, err)
		}
		if exists(path) {
			t.Error("directory should be in the trash")
		}

		// The stale registration is still listed, so undo has to reuse it
		// instead of falling back to a detached checkout that git refuses.
		fx.env.Git = fx.git
		if err := (removeWorktree{}).Undo(context.Background(), fx.env, en); err != nil {
			t.Fatalf("Undo after prune failure: %v", err)
		}
		fx.wantRestoredDirty(path)
	})
}

// failingRunner fails every git call whose arguments contain fail.
type failingRunner struct {
	gitx.Runner
	fail string
}

func (r failingRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	for _, a := range args {
		if a == r.fail {
			return "", &gitx.Error{Args: args, Dir: dir, ExitCode: 128, Stderr: "fatal: simulated " + r.fail + " failure"}
		}
	}
	return r.Runner.Run(ctx, dir, args...)
}

func TestRemoveWorktreeGitRefusalIsFailure(t *testing.T) {
	fx := newWTFixture(t)
	path := fx.add("feat", "feat")
	step, err := fx.plan(removeWorktree{}, fx.removeFinding(path))
	if err != nil {
		t.Fatal(err)
	}
	fx.env.Git = failingRunner{Runner: fx.git, fail: "remove"}
	en, err := removeWorktree{}.Apply(context.Background(), fx.env, step)
	if err == nil || !strings.Contains(err.Error(), "simulated remove failure") {
		t.Fatalf("err = %v", err)
	}
	if en.Status != session.StatusFailed || !exists(path) {
		t.Errorf("entry = %+v, exists = %v", en, exists(path))
	}
}

func TestRemoveWorktreeRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(fx *wtFixture) findings.Finding
		force bool
		want  string
	}{
		{"locked", func(fx *wtFixture) findings.Finding {
			p := fx.add("wt", "feat")
			fx.gitOut(fx.repo.Dir, "worktree", "lock", "--reason", "on usb stick", p)
			return fx.removeFinding(p)
		}, false, "on usb stick"},
		{"locked even with force", func(fx *wtFixture) findings.Finding {
			p := fx.add("wt", "feat")
			fx.gitOut(fx.repo.Dir, "worktree", "lock", p)
			return fx.removeFinding(p)
		}, true, "worktree_locked"},
		{"main worktree", func(fx *wtFixture) findings.Finding {
			return fx.removeFinding(fx.repo.Dir)
		}, true, "main or a bare worktree"},
		{"head moved", func(fx *wtFixture) findings.Finding {
			p := fx.add("wt", "feat")
			f := fx.removeFinding(p)
			fx.gitOut(p, "-c", "user.name=t", "-c", "user.email=t@t.invalid", "commit", "-q", "--allow-empty", "-m", "more")
			return f
		}, false, "worktree changed since the scan"},
		{"branch changed", func(fx *wtFixture) findings.Finding {
			f := fx.removeFinding(fx.add("wt", "feat"))
			f.Ref = "other"
			return f
		}, false, "worktree changed since the scan"},
		{"ignored file edited since the scan", editedSinceScan, false, "modified since the scan"},
		{"ignored file edited since the scan, even with force", editedSinceScan, true, "modified since the scan"},
		{"missing head in finding", func(fx *wtFixture) findings.Finding {
			f := fx.removeFinding(fx.add("wt", "feat"))
			delete(f.Meta, "head")
			return f
		}, false, "worktree changed since the scan"},
		{"no longer registered", func(fx *wtFixture) findings.Finding {
			p := fx.add("wt", "feat")
			f := fx.removeFinding(p)
			fx.gitOut(fx.repo.Dir, "worktree", "remove", p)
			return f
		}, false, "no longer a registered worktree"},
		{"directory missing", func(fx *wtFixture) findings.Finding {
			p := fx.add("wt", "feat")
			f := fx.removeFinding(p)
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			return f
		}, false, "directory is missing"},
		{"no repo in finding", func(fx *wtFixture) findings.Finding {
			f := fx.removeFinding(fx.add("wt", "feat"))
			delete(f.Meta, "repo")
			return f
		}, false, "no repository"},
		{"repo outside the guard", func(fx *wtFixture) findings.Finding {
			f := fx.removeFinding(fx.add("wt", "feat"))
			fx.roots = []string{filepath.Dir(f.Path)}
			fx.rebuildGuard()
			return f
		}, false, "outside the allowed scope"},
		{"worktree outside the guard", func(fx *wtFixture) findings.Finding {
			f := fx.removeFinding(fx.add("wt", "feat"))
			fx.roots = []string{fx.repo.Dir}
			fx.rebuildGuard()
			return f
		}, false, "outside the allowed scope"},
		{"blocking risk flag", func(fx *wtFixture) findings.Finding {
			f := fx.removeFinding(fx.add("wt", "feat"))
			f.RiskFlags = []findings.RiskFlag{findings.RiskWorktreeLocked}
			return f
		}, true, "worktree_locked"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWTFixture(t)
			fx.env.Force = tc.force
			f := tc.setup(fx)
			_, err := fx.plan(removeWorktree{}, f)
			wantSkip(t, err, tc.want)
			// Apply re-validates: it never acts on a finding Plan refuses.
			en, err := removeWorktree{}.Apply(context.Background(), fx.env, Step{Finding: f})
			if err != nil || en.Status != session.StatusSkipped {
				t.Errorf("Apply = %+v, %v; want a skipped entry", en, err)
			}
			if exists(f.Path) && f.Path != fx.repo.Dir {
				if _, statErr := os.Stat(filepath.Join(f.Path, ".git")); statErr != nil {
					t.Error("refused worktree was modified")
				}
			}
		})
	}
}

func TestRemoveWorktreeApplyRevalidatesDirtyState(t *testing.T) {
	fx := newWTFixture(t)
	path := fx.add("wt", "feat")
	step, err := fx.plan(removeWorktree{}, fx.removeFinding(path))
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, path, "late.txt", "appeared after planning")
	en, err := removeWorktree{}.Apply(context.Background(), fx.env, step)
	if err != nil || en.Status != session.StatusSkipped || !strings.Contains(en.Error, "uncommitted changes") {
		t.Fatalf("entry = %+v, err = %v", en, err)
	}
	if !exists(filepath.Join(path, "late.txt")) {
		t.Error("late file lost")
	}
}

func TestRemoveWorktreeIgnoredFilesDoNotBlock(t *testing.T) {
	fx := newWTFixture(t)
	fx.repo.WriteFile(".gitignore", "build/\n")
	fx.repo.CommitAll("ignore build", testutil.BaseTime)
	path := fx.add("wt", "feat")
	testutil.WriteFile(t, path, "build/out.bin", "artifact")
	en, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
	if err != nil || en.Status != session.StatusApplied {
		t.Fatalf("entry = %+v, err = %v", en, err)
	}
	if exists(path) {
		t.Error("worktree with only ignored files should be removed")
	}
}

// editedSinceScan returns a finding recorded while the worktree was old and
// then rewrites an ignored file in place. git status never lists ignored
// files, so only the newest mtime can reveal the edit.
func editedSinceScan(fx *wtFixture) findings.Finding {
	fx.repo.WriteFile(".gitignore", "cache.bin\n")
	fx.repo.CommitAll("ignore cache", testutil.BaseTime)
	p := fx.add("wt", "feat")
	ignored := testutil.WriteFile(fx.t, p, "cache.bin", "old")
	testutil.SetMTime(fx.t, ignored, testutil.BaseTime)
	scanned := testutil.BaseTime
	f := fx.removeFinding(p)
	f.LastModified = &scanned
	testutil.WriteFile(fx.t, p, "cache.bin", "new")
	testutil.SetMTime(fx.t, ignored, testutil.BaseTime.AddDate(0, 0, 50))
	return f
}

func TestRemoveWorktreeUndoTrashedRefusals(t *testing.T) {
	fx := newWTFixture(t)
	fx.env.Force = true
	path := fx.add("feat", "feat")
	testutil.WriteFile(t, path, "wip.txt", "wip")
	en, err := fx.apply(removeWorktree{}, fx.removeFinding(path))
	if err != nil {
		t.Fatal(err)
	}
	act := removeWorktree{}

	t.Run("conflict leaves trash copy and no registration", func(t *testing.T) {
		testutil.WriteFile(t, path, "squatter.txt", "x")
		// A non-empty path is refused before anything is created.
		if err := act.Undo(context.Background(), fx.env, en); err == nil {
			t.Fatal("expected refusal")
		}
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("restore failure rolls the registration back", func(t *testing.T) {
		bad := en
		rec := *en.Trash
		rec.StoredPath = filepath.Join(fx.quar, "does-not-exist")
		bad.Trash = &rec
		err := act.Undo(context.Background(), fx.env, bad)
		if err == nil {
			t.Fatal("expected failure")
		}
		if fx.registered(path) || exists(path) {
			t.Error("placeholder registration left behind")
		}
		if _, statErr := os.Stat(en.Trash.StoredPath); statErr != nil {
			t.Errorf("trash copy touched: %v", statErr)
		}
	})
	t.Run("record for another path", func(t *testing.T) {
		bad := en
		rec := *en.Trash
		rec.OriginalPath = filepath.Join(filepath.Dir(path), "elsewhere")
		bad.Trash = &rec
		if err := act.Undo(context.Background(), fx.env, bad); err == nil {
			t.Fatal("expected refusal")
		}
	})
	t.Run("restore conflict is passed through", func(t *testing.T) {
		// Occupy the path after the placeholder step by using a trasher whose
		// Restore reports a conflict.
		fx.env.TrasherFor = func(config.TrashStrategy) (trash.Trasher, error) {
			return conflictTrasher{}, nil
		}
		err := act.Undo(context.Background(), fx.env, en)
		if !errors.Is(err, trash.ErrRestoreConflict) {
			t.Fatalf("err = %v", err)
		}
		if fx.registered(path) {
			t.Error("registration not rolled back")
		}
	})
}

type conflictTrasher struct{}

func (conflictTrasher) Strategy() config.TrashStrategy { return config.StrategyQuarantine }
func (conflictTrasher) Remove(context.Context, string) (trash.Record, error) {
	return trash.Record{}, errors.New("unused")
}
func (conflictTrasher) Restore(context.Context, trash.Record) error {
	return trash.ErrRestoreConflict
}

// missingWorktree registers a worktree and deletes its directory.
func (fx *wtFixture) missingWorktree(rel, branch string) findings.Finding {
	fx.t.Helper()
	p := fx.add(rel, branch)
	f := fx.finding(p, findings.ActionPruneWorktrees)
	f.Kind = findings.KindWorktreeMissing
	if err := os.RemoveAll(p); err != nil {
		fx.t.Fatal(err)
	}
	return f
}

func TestPruneWorktreesRemovesOnlyMissing(t *testing.T) {
	fx := newWTFixture(t)
	keep := fx.add("keep", "keep")
	f := fx.missingWorktree("gone", "gone")
	act := pruneWorktrees{}

	step, err := fx.plan(act, f)
	if err != nil {
		t.Fatal(err)
	}
	if step.Command != "git worktree prune" {
		t.Errorf("command = %q", step.Command)
	}
	en, err := act.Apply(context.Background(), fx.env, step)
	if err != nil {
		t.Fatal(err)
	}
	if en.Status != session.StatusApplied || en.Restorable || en.Trash != nil {
		t.Errorf("entry = %+v", en)
	}
	if fx.registered(f.Path) {
		t.Error("missing worktree still registered")
	}
	if !fx.registered(keep) || !exists(keep) {
		t.Error("existing worktree was touched")
	}
	wantContains(t, en.RecoveryHint, "metadata", f.Path, "git worktree add", "gone", "untouched on disk")
	fx.gitOut(fx.repo.Dir, "branch", "-d", "gone")
	if err := act.Undo(context.Background(), fx.env, en); err == nil {
		t.Error("Undo should report that pruning is not reversible")
	}
}

func TestPruneWorktreesSkips(t *testing.T) {
	tests := []struct {
		name  string
		setup func(fx *wtFixture) findings.Finding
		want  string
	}{
		{"directory came back", func(fx *wtFixture) findings.Finding {
			f := fx.missingWorktree("gone", "gone")
			if err := os.MkdirAll(f.Path, 0o755); err != nil {
				t.Fatal(err)
			}
			return f
		}, "not prunable any more"},
		{"locked", func(fx *wtFixture) findings.Finding {
			p := fx.add("gone", "gone")
			fx.gitOut(fx.repo.Dir, "worktree", "lock", p)
			f := fx.finding(p, findings.ActionPruneWorktrees)
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			return f
		}, "not prunable any more"},
		{"already pruned", func(fx *wtFixture) findings.Finding {
			f := fx.missingWorktree("gone", "gone")
			fx.gitOut(fx.repo.Dir, "worktree", "prune")
			return f
		}, "not prunable any more"},
		{"existing worktree", func(fx *wtFixture) findings.Finding {
			return fx.finding(fx.add("live", "live"), findings.ActionPruneWorktrees)
		}, "not prunable any more"},
		{"main worktree", func(fx *wtFixture) findings.Finding {
			return fx.finding(fx.repo.Dir, findings.ActionPruneWorktrees)
		}, "not prunable any more"},
		{"no repo", func(fx *wtFixture) findings.Finding {
			f := fx.missingWorktree("gone", "gone")
			delete(f.Meta, "repo")
			return f
		}, "no repository"},
		{"repo outside guard", func(fx *wtFixture) findings.Finding {
			f := fx.missingWorktree("gone", "gone")
			fx.roots = []string{filepath.Dir(f.Path)}
			fx.rebuildGuard()
			return f
		}, "outside the allowed scope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWTFixture(t)
			f := tc.setup(fx)
			_, err := fx.plan(pruneWorktrees{}, f)
			wantSkip(t, err, tc.want)
			en, err := pruneWorktrees{}.Apply(context.Background(), fx.env, Step{Finding: f})
			if err != nil || en.Status != session.StatusSkipped {
				t.Errorf("Apply = %+v, %v; want a skipped entry", en, err)
			}
		})
	}
}

// TestPruneWorktreesSecondStepSkipped mirrors the executor: it re-plans
// before every step, and the first prune removes all missing entries.
func TestPruneWorktreesSecondStepSkipped(t *testing.T) {
	fx := newWTFixture(t)
	f1 := fx.missingWorktree("one", "one")
	f2 := fx.missingWorktree("two", "two")
	act := pruneWorktrees{}

	en, err := fx.apply(act, f1)
	if err != nil || en.Status != session.StatusApplied {
		t.Fatalf("first: %+v, %v", en, err)
	}
	if !strings.Contains(en.RecoveryHint, f1.Path) || !strings.Contains(en.RecoveryHint, f2.Path) {
		t.Errorf("hint should name both pruned entries: %q", en.RecoveryHint)
	}
	_, err = fx.plan(act, f2)
	wantSkip(t, err, "not prunable any more")
}

func TestPruneWorktreesVerifyRefusesUnexpectedRemoval(t *testing.T) {
	live := gitx.Worktree{Path: filepath.FromSlash("/r/live")}
	gone := gitx.Worktree{Path: filepath.FromSlash("/r/gone"), Prunable: true}
	locked := gitx.Worktree{Path: filepath.FromSlash("/r/locked"), Prunable: true, Locked: true}

	if _, err := verifyPruned([]gitx.Worktree{live, gone}, []gitx.Worktree{live}, gone.Path); err != nil {
		t.Errorf("expected success: %v", err)
	}
	if _, err := verifyPruned([]gitx.Worktree{live, gone}, []gitx.Worktree{}, gone.Path); err == nil {
		t.Error("removal of a non-prunable entry must fail")
	}
	if _, err := verifyPruned([]gitx.Worktree{gone, locked}, []gitx.Worktree{}, gone.Path); err == nil {
		t.Error("removal of a locked entry must fail")
	}
	if _, err := verifyPruned([]gitx.Worktree{gone}, []gitx.Worktree{gone}, gone.Path); err == nil {
		t.Error("a target that is still registered must fail")
	}
}

func TestWorktreeHelpers(t *testing.T) {
	if got := refLabel(""); got != "detached" {
		t.Errorf("refLabel = %q", got)
	}
	hint := readdHint(map[string]string{"worktree": "/a b/wt", "head": "abc"})
	if hint != "git worktree add --detach '/a b/wt' abc" {
		t.Errorf("detached hint = %q", hint)
	}
	hint = readdHint(map[string]string{"worktree": "/wt", "branch": "feat/x", "head": "abc"})
	if hint != "git worktree add /wt feat/x" {
		t.Errorf("branch hint = %q", hint)
	}
}
