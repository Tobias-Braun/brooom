package action

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/trash"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// trashFixture is a sandbox for the trash action: a scan root inside a temp
// dir, the Brooom home and the user's home inside that root (so the guard
// covers them and only the static refusals can stop a removal), and a
// quarantine trasher. Nothing here touches the real home or OS trash.
type trashFixture struct {
	t        *testing.T
	root     string
	brooom   string
	userHome string
	env      *Env
	strategy config.TrashStrategy
}

func newTrashFixture(t *testing.T) *trashFixture {
	t.Helper()
	root := testutil.ResolvedTempDir(t)
	fx := &trashFixture{
		t: t, root: root,
		brooom:   filepath.Join(root, "brooom-home"),
		userHome: filepath.Join(root, "users", "me"),
		strategy: config.StrategyQuarantine,
	}
	for _, d := range []string{fx.brooom, fx.userHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(config.HomeEnv, fx.brooom)
	t.Setenv("HOME", fx.userHome)
	t.Setenv("USERPROFILE", fx.userHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))

	fx.setOpen(func(context.Context, []string) (map[string]bool, error) { return nil, nil })
	guard, err := scope.NewGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	git, err := gitx.NewExecRunner()
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	fx.env = &Env{
		Guard: guard,
		Git:   git,
		Trasher: func(string) (trash.Trasher, error) {
			return fx.trasher(fx.strategy)
		},
		TrasherFor: fx.trasher,
	}
	return fx
}

// setOpen replaces the open-file check for the test.
func (fx *trashFixture) setOpen(fn func(context.Context, []string) (map[string]bool, error)) {
	fx.t.Helper()
	old := openFilesFn
	openFilesFn = fn
	fx.t.Cleanup(func() { openFilesFn = old })
}

func (fx *trashFixture) trasher(s config.TrashStrategy) (trash.Trasher, error) {
	return trash.New(s, trash.Options{SessionID: "20260930-120000-test", QuarantineDir: filepath.Join(fx.brooom, "quarantine")})
}

func (fx *trashFixture) path(rel string) string {
	return filepath.Join(fx.root, filepath.FromSlash(rel))
}

func (fx *trashFixture) write(rel, content string) string {
	return testutil.WriteFile(fx.t, fx.root, rel, content)
}

func (fx *trashFixture) mkdir(rel string) string {
	fx.t.Helper()
	p := fx.path(rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		fx.t.Fatal(err)
	}
	return p
}

func trashFinding(path string) findings.Finding {
	return findings.Finding{
		ID: findings.NewID("build-artifacts", findings.KindDir, path, ""), Detector: "build-artifacts",
		Path: path, Kind: findings.KindDir, SizeBytes: 1,
		SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash},
	}
}

// wantSkip asserts err wraps ErrSkipped and its reason contains want.
func wantSkip(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("err = %v, want a skip containing %q", err, want)
	}
	if reason := skipReason(err); !strings.Contains(reason, want) {
		t.Fatalf("skip reason = %q, want it to contain %q", reason, want)
	}
}

func TestTrashRegistered(t *testing.T) {
	a, ok := Get(findings.ActionTrash)
	if !ok || a.Type() != findings.ActionTrash {
		t.Fatalf("trash action not registered: %v %v", a, ok)
	}
}

func TestTrashPlanApplyUndoHappyPath(t *testing.T) {
	tests := []struct {
		name  string
		setup func(fx *trashFixture) string
		isDir bool
	}{
		{"file", func(fx *trashFixture) string { return fx.write("proj/app.log", "log line\n") }, false},
		{"dir", func(fx *trashFixture) string {
			fx.write("proj/node_modules/a/index.js", "x")
			fx.write("proj/node_modules/b.js", "yy")
			return fx.path("proj/node_modules")
		}, true},
		{"symlink", func(fx *trashFixture) string {
			target := fx.write("elsewhere/data.txt", "precious")
			link := fx.path("proj/link")
			fx.mkdir("proj")
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("cannot create symlinks here: %v", err)
			}
			return link
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newTrashFixture(t)
			path := tt.setup(fx)
			en := planAndApply(t, fx, path)
			checkAppliedEntry(t, en, tt.isDir)
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("original still exists after apply: %v", err)
			}
			if err := (trashAction{}).Undo(context.Background(), fx.env, en); err != nil {
				t.Fatalf("Undo: %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("original not restored: %v", err)
			}
			if tt.name == "symlink" {
				assertFileContent(t, fx.path("elsewhere/data.txt"), "precious")
			}
		})
	}
}

func planAndApply(t *testing.T, fx *trashFixture, path string) session.Entry {
	t.Helper()
	step, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(path))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !strings.Contains(step.Description, "to quarantine") || !strings.Contains(step.Description, filepath.Base(path)) {
		t.Errorf("Description = %q", step.Description)
	}
	if !strings.HasPrefix(step.Command, displayVerb(config.StrategyQuarantine)) {
		t.Errorf("Command = %q", step.Command)
	}
	en, err := trashAction{}.Apply(context.Background(), fx.env, step)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return en
}

func checkAppliedEntry(t *testing.T, en session.Entry, isDir bool) {
	t.Helper()
	if en.Status != session.StatusApplied || en.Trash == nil || !en.Restorable || en.Action != findings.ActionTrash {
		t.Fatalf("entry = %+v", en)
	}
	if en.Trash.IsDir != isDir || en.SizeBytes != en.Trash.SizeBytes || en.At.IsZero() {
		t.Errorf("entry record = %+v size=%d at=%v", en.Trash, en.SizeBytes, en.At)
	}
	if want := "restore from " + en.Trash.StoredPath; en.RecoveryHint != want {
		t.Errorf("RecoveryHint = %q, want %q", en.RecoveryHint, want)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	if b, err := os.ReadFile(path); err != nil || string(b) != want {
		t.Fatalf("%s = %q %v, want %q", path, b, err, want)
	}
}

func TestTrashPlanSkips(t *testing.T) {
	tests := []struct {
		name   string
		force  bool
		setup  func(fx *trashFixture) findings.Finding
		reason string
	}{
		{"outside scope", false, func(fx *trashFixture) findings.Finding {
			return trashFinding(testutil.WriteFile(t, testutil.ResolvedTempDir(t), "x.log", "x"))
		}, "outside allowed roots"},
		{"allowed root", false, func(fx *trashFixture) findings.Finding {
			g, _ := scope.NewGuard(fx.mkdir("scan"), fx.root)
			fx.env.Guard = g
			return trashFinding(fx.path("scan"))
		}, "allowed root"},
		{"brooom home", true, func(fx *trashFixture) findings.Finding { return trashFinding(fx.brooom) }, "Brooom home"},
		{"brooom sessions", true, func(fx *trashFixture) findings.Finding {
			return trashFinding(fx.mkdir("brooom-home/sessions"))
		}, "session or quarantine"},
		{"brooom quarantine file", true, func(fx *trashFixture) findings.Finding {
			return trashFinding(fx.write("brooom-home/quarantine/s/1/a", "a"))
		}, "session or quarantine"},
		{"ancestor of brooom home", true, func(fx *trashFixture) findings.Finding {
			return trashFinding(fx.path("brooom-home"))
		}, "Brooom home"},
		{"user home", true, func(fx *trashFixture) findings.Finding { return trashFinding(fx.userHome) }, "home directory"},
		{"ancestor of user home", true, func(fx *trashFixture) findings.Finding { return trashFinding(fx.path("users")) }, "home directory"},
		{"gone", false, func(fx *trashFixture) findings.Finding { return trashFinding(fx.path("nope/build")) }, "already gone"},
		{"direct .git child", true, func(fx *trashFixture) findings.Finding {
			// Not a valid gitdir pointer, so the repository-root check does not see it and the walk must.
			fx.write("vendor/.git", "opaque\n")
			return trashFinding(fx.path("vendor"))
		}, "contains a git repository (.git at .git)"},
		{"deeply nested repo", false, func(fx *trashFixture) findings.Finding {
			fx.write("dir/sub/nested/.git/HEAD", "ref: refs/heads/main\n")
			return trashFinding(fx.path("dir"))
		}, "contains a git repository (.git at sub/nested/.git)"},
		{"deeply nested repo with force", true, func(fx *trashFixture) findings.Finding {
			fx.write("dir/sub/nested/.git", "gitdir: /x\n")
			return trashFinding(fx.path("dir"))
		}, "contains a git repository (.git at sub/nested/.git)"},
		{"nested mercurial repo", true, func(fx *trashFixture) findings.Finding {
			fx.write("dir/sub/.hg/store", "x")
			return trashFinding(fx.path("dir"))
		}, "contains a nested repository (.hg at sub/.hg)"},
		{"nested jujutsu repo", true, func(fx *trashFixture) findings.Finding {
			fx.write("dir/.jj/repo", "x")
			return trashFinding(fx.path("dir"))
		}, "contains a nested repository (.jj at .jj)"},
		{"nested subversion checkout", true, func(fx *trashFixture) findings.Finding {
			fx.write("dir/sub/.svn/wc.db", "x")
			return trashFinding(fx.path("dir"))
		}, "contains a nested repository (.svn at sub/.svn)"},
		{"nested bare repo", true, func(fx *trashFixture) findings.Finding {
			fx.write("dir/sub/clone.git/HEAD", "ref: refs/heads/main\n")
			fx.write("dir/sub/clone.git/objects/pack/p", "x")
			fx.write("dir/sub/clone.git/refs/heads/main", "x")
			return trashFinding(fx.path("dir"))
		}, "contains a bare git repository (at sub/clone.git)"},
		{"target is a bare repo", true, func(fx *trashFixture) findings.Finding {
			fx.write("clone.git/HEAD", "ref: refs/heads/main\n")
			fx.write("clone.git/objects/x", "x")
			fx.write("clone.git/refs/x", "x")
			return trashFinding(fx.path("clone.git"))
		}, "contains a bare git repository (at .)"},
		{"open file", false, func(fx *trashFixture) findings.Finding {
			p := fx.write("proj/run.log", "x")
			fx.setOpen(func(_ context.Context, paths []string) (map[string]bool, error) {
				return map[string]bool{paths[0]: true}, nil
			})
			return trashFinding(p)
		}, "file is open by a process"},
		{"open file with force", true, func(fx *trashFixture) findings.Finding {
			p := fx.write("proj/run.log", "x")
			fx.setOpen(func(_ context.Context, paths []string) (map[string]bool, error) {
				return map[string]bool{paths[0]: true}, nil
			})
			return trashFinding(p)
		}, "file is open by a process"},
		{"open file reported next to incomplete", false, func(fx *trashFixture) findings.Finding {
			p := fx.write("proj/run.log", "x")
			fx.setOpen(func(_ context.Context, paths []string) (map[string]bool, error) {
				return map[string]bool{paths[0]: true}, procs.ErrIncomplete
			})
			return trashFinding(p)
		}, "file is open by a process"},
		{"blocking flag", false, func(fx *trashFixture) findings.Finding {
			f := trashFinding(fx.write("proj/a.log", "x"))
			f.RiskFlags = []findings.RiskFlag{findings.RiskWorktreeDirty}
			return f
		}, "worktree_dirty"},
		{"non-overridable flag with force", true, func(fx *trashFixture) findings.Finding {
			f := trashFinding(fx.write("proj/a.log", "x"))
			f.RiskFlags = []findings.RiskFlag{findings.RiskFileOpen}
			return f
		}, "not overridable"},
		{"delete strategy untracked", false, deleteUntracked, "refusing to permanently delete untracked files"},
		{"delete strategy untracked with force", true, deleteUntracked, "refusing to permanently delete untracked files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newTrashFixture(t)
			fx.env.Force = tt.force
			f := tt.setup(fx)
			_, err := trashAction{}.Plan(context.Background(), fx.env, f)
			wantSkip(t, err, tt.reason)
		})
	}
}

// deleteUntracked prepares a finding flagged untracked with the delete
// strategy selected.
func deleteUntracked(fx *trashFixture) findings.Finding {
	fx.strategy = config.StrategyDelete
	f := trashFinding(fx.write("proj/big.bin", "data"))
	f.Meta = map[string]string{"user_data_risk": "untracked"}
	return f
}

func TestTrashDeleteUntrackedAllowedWithQuarantine(t *testing.T) {
	fx := newTrashFixture(t)
	fx.env.Force = true
	f := deleteUntracked(fx)
	fx.strategy = config.StrategyQuarantine
	step, err := trashAction{}.Plan(context.Background(), fx.env, f)
	if err != nil {
		t.Fatalf("Plan with quarantine: %v", err)
	}
	if !strings.Contains(step.Description, "to quarantine") {
		t.Errorf("Description = %q", step.Description)
	}
}

func TestTrashRepoRootAndGitRefused(t *testing.T) {
	repo := testutil.NewRepo(t)
	// The guard allows the repo's parent, so the repo root is not an allowed
	// root and only the repository check can stop it.
	guard, err := scope.NewGuard(filepath.Dir(repo.Dir))
	if err != nil {
		t.Fatal(err)
	}
	fx := newTrashFixture(t)
	fx.env.Guard = guard
	fx.env.Force = true

	for name, path := range map[string]string{
		"repo root":      repo.Dir,
		".git dir":       filepath.Join(repo.Dir, ".git"),
		"inside .git":    filepath.Join(repo.Dir, ".git", "objects"),
		".git hook file": filepath.Join(repo.Dir, ".git", "HEAD"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(path))
			wantSkip(t, err, "refusing to remove")
		})
	}
}

func TestTrashVolumeRootRefused(t *testing.T) {
	fx := newTrashFixture(t)
	vol := filepath.VolumeName(fx.root) + string(filepath.Separator)
	wantSkip(t, refuseTarget(fx.env, vol), "filesystem root")
}

func TestTrashOpenFileChecks(t *testing.T) {
	tests := []struct {
		name     string
		res      func(path string) map[string]bool
		err      error
		wantNote string
		wantSkip bool
	}{
		{"unavailable", func(string) map[string]bool { return nil }, procs.ErrUnavailable, "open-file check unavailable", false},
		{"incomplete false entries", func(p string) map[string]bool { return map[string]bool{p: false} }, procs.ErrIncomplete, "open-file check incomplete", false},
		{"incomplete true entry", func(p string) map[string]bool { return map[string]bool{p: true} }, procs.ErrIncomplete, "", true},
		{"unexpected error is unknown", func(string) map[string]bool { return nil }, errors.New("boom"), "open-file check failed", false},
		{"not open", func(p string) map[string]bool { return map[string]bool{p: false} }, nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newTrashFixture(t)
			p := fx.write("proj/a.log", "x")
			fx.setOpen(func(_ context.Context, paths []string) (map[string]bool, error) {
				return tt.res(paths[0]), tt.err
			})
			step, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(p))
			if tt.wantSkip {
				wantSkip(t, err, "file is open by a process")
				return
			}
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if tt.wantNote == "" && strings.Contains(step.Description, "open-file") {
				t.Errorf("unexpected note in %q", step.Description)
			}
			if !strings.Contains(step.Description, tt.wantNote) {
				t.Errorf("Description = %q, want note %q", step.Description, tt.wantNote)
			}
		})
	}
}

func TestTrashRiskFlagsWithForce(t *testing.T) {
	fx := newTrashFixture(t)
	f := trashFinding(fx.write("proj/a.log", "x"))
	f.RiskFlags = []findings.RiskFlag{findings.RiskWorktreeDirty}
	fx.env.Force = true
	if _, err := (trashAction{}).Plan(context.Background(), fx.env, f); err != nil {
		t.Fatalf("Plan with force: %v", err)
	}
}

// failingGit lets tests make the tracked-files check fail.
type failingGit struct{}

func (failingGit) Run(context.Context, string, ...string) (string, error) {
	return "", errors.New("git exploded")
}

// TestTrashTrackedFilesForeignGitDir checks the apply-time tracked check
// against an environment whose GIT_DIR points at another repository.
func TestTrashTrackedFilesForeignGitDir(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("src/a.go", "package a\n")
	repo.CommitAll("add src", testutil.BaseTime)
	foreign := testutil.NewRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(foreign.Dir, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(foreign.Dir, ".git", "index"))
	fx := newTrashFixture(t)
	guard, err := scope.NewGuard(filepath.Dir(repo.Dir))
	if err != nil {
		t.Fatal(err)
	}
	fx.env.Guard = guard
	_, err = trashAction{}.Plan(context.Background(), fx.env, trashFinding(filepath.Join(repo.Dir, "src", "a.go")))
	wantSkip(t, err, "contains files tracked by git")
}

// TestTrashTrackedLiteralPathspec checks that a path with pathspec magic
// characters is matched literally: a glob-like directory must not report
// files tracked elsewhere as its own.
func TestTrashTrackedLiteralPathspec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file names cannot contain an asterisk")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "tracked\n")
	repo.CommitAll("add", testutil.BaseTime)
	repo.WriteFile("*.txt", "untracked\n")
	fx := newTrashFixture(t)
	guard, err := scope.NewGuard(filepath.Dir(repo.Dir))
	if err != nil {
		t.Fatal(err)
	}
	fx.env.Guard = guard
	if _, err := (trashAction{}).Plan(context.Background(), fx.env, trashFinding(filepath.Join(repo.Dir, "*.txt"))); err != nil {
		t.Fatalf("literal glob-named file must not match tracked a.txt: %v", err)
	}
}

func TestTrashTrackedFiles(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("src/a.go", "package a\n")
	repo.WriteFile(".gitignore", "build/\n")
	repo.CommitAll("add src", testutil.BaseTime)
	repo.WriteFile("build/out.bin", "binary")

	newFx := func(t *testing.T, force bool) *trashFixture {
		fx := newTrashFixture(t)
		guard, err := scope.NewGuard(filepath.Dir(repo.Dir))
		if err != nil {
			t.Fatal(err)
		}
		fx.env.Guard = guard
		fx.env.Force = force
		return fx
	}
	ctx := context.Background()

	t.Run("tracked dir skipped", func(t *testing.T) {
		_, err := trashAction{}.Plan(ctx, newFx(t, false).env, trashFinding(filepath.Join(repo.Dir, "src")))
		wantSkip(t, err, "contains files tracked by git")
	})
	t.Run("tracked file skipped", func(t *testing.T) {
		_, err := trashAction{}.Plan(ctx, newFx(t, false).env, trashFinding(filepath.Join(repo.Dir, "src", "a.go")))
		wantSkip(t, err, "contains files tracked by git")
	})
	t.Run("tracked dir with force", func(t *testing.T) {
		step, err := trashAction{}.Plan(ctx, newFx(t, true).env, trashFinding(filepath.Join(repo.Dir, "src")))
		if err != nil || !strings.Contains(step.Description, "tracked files") {
			t.Fatalf("step = %+v err = %v", step, err)
		}
	})
	t.Run("untracked ignored dir allowed", func(t *testing.T) {
		step, err := trashAction{}.Plan(ctx, newFx(t, false).env, trashFinding(filepath.Join(repo.Dir, "build")))
		if err != nil || strings.Contains(step.Description, "tracked") {
			t.Fatalf("step = %+v err = %v", step, err)
		}
	})
	t.Run("git failure means unknown", func(t *testing.T) {
		fx := newFx(t, false)
		fx.env.Git = failingGit{}
		_, err := trashAction{}.Plan(ctx, fx.env, trashFinding(filepath.Join(repo.Dir, "build")))
		wantSkip(t, err, "tracked files cannot be ruled out")
	})
	t.Run("git failure with force", func(t *testing.T) {
		fx := newFx(t, true)
		fx.env.Git = failingGit{}
		step, err := trashAction{}.Plan(ctx, fx.env, trashFinding(filepath.Join(repo.Dir, "build")))
		if err != nil || !strings.Contains(step.Description, "tracked files") {
			t.Fatalf("step = %+v err = %v", step, err)
		}
	})
	t.Run("no git runner means unknown", func(t *testing.T) {
		fx := newFx(t, false)
		fx.env.Git = nil
		_, err := trashAction{}.Plan(ctx, fx.env, trashFinding(filepath.Join(repo.Dir, "build")))
		wantSkip(t, err, "tracked files cannot be ruled out")
	})
}

func TestTrashSizeRefresh(t *testing.T) {
	fx := newTrashFixture(t)
	fx.write("out/a.bin", strings.Repeat("a", 5000))
	fx.write("out/sub/b.bin", strings.Repeat("b", 9000))
	fx.mkdir("out/empty")
	// The link is best effort: Windows may not permit creating it.
	_ = os.Symlink(fx.path("out/a.bin"), fx.path("out/sub/link"))
	dir := fx.path("out")
	want, err := walk.DirSize(context.Background(), dir, walk.Options{Fresh: true})
	if err != nil {
		t.Fatal(err)
	}

	f := trashFinding(dir)
	f.SizeBytes = 1 // stale value from the scan
	step, err := trashAction{}.Plan(context.Background(), fx.env, f)
	if err != nil {
		t.Fatal(err)
	}
	if step.Finding.SizeBytes != want.SizeBytes || want.SizeBytes <= 1 {
		t.Errorf("SizeBytes = %d, want walk.DirSize %d", step.Finding.SizeBytes, want.SizeBytes)
	}
	if step.Finding.LastModified == nil || step.Finding.LastModified.IsZero() {
		t.Error("LastModified not refreshed")
	}
	if step.Finding.Path != dir {
		t.Errorf("Path = %q, want resolved %q", step.Finding.Path, dir)
	}
	if f.SizeBytes != 1 {
		t.Error("Plan mutated the input finding")
	}
}

func TestTrashHardLinksCountedOnce(t *testing.T) {
	fx := newTrashFixture(t)
	a := fx.write("out/a.bin", strings.Repeat("a", 8192))
	if err := os.Link(a, fx.path("out/b.bin")); err != nil {
		t.Skipf("cannot create hard links here: %v", err)
	}
	want, err := walk.DirSize(context.Background(), fx.path("out"), walk.Options{Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	m, err := sizeAndNestedVCS(context.Background(), fx.path("out"))
	if err != nil {
		t.Fatal(err)
	}
	if m.size != want.SizeBytes {
		t.Errorf("size = %d, want %d", m.size, want.SizeBytes)
	}
}

func TestTrashUnreadableDirectoryIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("cannot make a directory unreadable on Windows or as root")
	}
	fx := newTrashFixture(t)
	fx.write("out/locked/f", "x")
	locked := fx.path("out/locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(fx.path("out")))
	wantSkip(t, err, "nested repository cannot be ruled out")
}

// stubTrasher is a scriptable trash.Trasher.
type stubTrasher struct {
	strategy   config.TrashStrategy
	removeRec  trash.Record
	removeErr  error
	restoreErr error
	removed    []string
	removeCtx  []context.Context
	restored   []trash.Record
}

func (s *stubTrasher) Strategy() config.TrashStrategy { return s.strategy }

func (s *stubTrasher) Remove(ctx context.Context, path string) (trash.Record, error) {
	s.removeCtx = append(s.removeCtx, ctx)
	s.removed = append(s.removed, path)
	return s.removeRec, s.removeErr
}

func (s *stubTrasher) Restore(_ context.Context, r trash.Record) error {
	s.restored = append(s.restored, r)
	return s.restoreErr
}

func (fx *trashFixture) useStub(s *stubTrasher) {
	fx.env.Trasher = func(string) (trash.Trasher, error) { return s, nil }
	fx.env.TrasherFor = func(config.TrashStrategy) (trash.Trasher, error) { return s, nil }
}

func TestTrashApplyFailures(t *testing.T) {
	t.Run("remove error", func(t *testing.T) {
		fx := newTrashFixture(t)
		p := fx.write("proj/a.log", "x")
		boom := errors.New("disk on fire")
		fx.useStub(&stubTrasher{strategy: config.StrategyQuarantine, removeErr: boom})
		step, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(p))
		if err != nil {
			t.Fatal(err)
		}
		en, err := trashAction{}.Apply(context.Background(), fx.env, step)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if en.Status != session.StatusFailed || !strings.Contains(en.Error, "disk on fire") || en.Trash != nil {
			t.Fatalf("entry = %+v", en)
		}
	})
	t.Run("partial record is kept", func(t *testing.T) {
		fx := newTrashFixture(t)
		p := fx.write("proj/a.log", "x")
		fx.useStub(&stubTrasher{
			strategy:  config.StrategyQuarantine,
			removeRec: trash.Record{Strategy: config.StrategyQuarantine, OriginalPath: p, StoredPath: "/q/1/a.log"},
			removeErr: errors.New("source not fully removed"),
		})
		en, err := trashAction{}.Apply(context.Background(), fx.env, Step{Finding: trashFinding(p)})
		if err == nil || en.Status != session.StatusFailed || en.Trash == nil || !strings.Contains(en.RecoveryHint, "/q/1/a.log") {
			t.Fatalf("entry = %+v err = %v", en, err)
		}
		if en.Restorable {
			t.Error("a failed entry must not be restorable")
		}
	})
	t.Run("path vanished before apply", func(t *testing.T) {
		fx := newTrashFixture(t)
		stub := &stubTrasher{strategy: config.StrategyQuarantine}
		fx.useStub(stub)
		en, err := trashAction{}.Apply(context.Background(), fx.env, Step{Finding: trashFinding(fx.path("proj/gone.log"))})
		if err != nil || en.Status != session.StatusSkipped || en.Error != "already gone" {
			t.Fatalf("entry = %+v err = %v", en, err)
		}
		if len(stub.removed) != 0 {
			t.Error("Remove was called for a vanished path")
		}
	})
	t.Run("path vanished during remove", func(t *testing.T) {
		fx := newTrashFixture(t)
		p := fx.write("proj/a.log", "x")
		stub := &stubTrasher{strategy: config.StrategyQuarantine}
		// The path disappears after Apply's own stat but before Remove runs.
		fx.env.Trasher = func(string) (trash.Trasher, error) {
			_ = os.Remove(p)
			stub.removeErr = fmt.Errorf("move %s: %w", p, fs.ErrNotExist)
			return stub, nil
		}
		en, err := trashAction{}.Apply(context.Background(), fx.env, Step{Finding: trashFinding(p)})
		if err != nil || en.Status != session.StatusSkipped {
			t.Fatalf("entry = %+v err = %v", en, err)
		}
	})
	t.Run("apply re-checks refusals", func(t *testing.T) {
		fx := newTrashFixture(t)
		stub := &stubTrasher{strategy: config.StrategyQuarantine}
		fx.useStub(stub)
		for _, path := range []string{fx.root, fx.userHome, testutil.ResolvedTempDir(t)} {
			en, err := trashAction{}.Apply(context.Background(), fx.env, Step{Finding: trashFinding(path)})
			if err == nil || en.Status != session.StatusFailed {
				t.Errorf("Apply(%s): entry = %+v err = %v", path, en, err)
			}
		}
		if len(stub.removed) != 0 {
			t.Errorf("Remove called for refused paths: %v", stub.removed)
		}
	})
	t.Run("trasher factory error", func(t *testing.T) {
		fx := newTrashFixture(t)
		p := fx.write("proj/a.log", "x")
		fx.env.Trasher = func(string) (trash.Trasher, error) { return nil, errors.New("no such strategy") }
		_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(p))
		if err == nil || errors.Is(err, ErrSkipped) {
			t.Fatalf("Plan err = %v, want a hard error", err)
		}
	})
}

// TestTrashApplyDeleteStrategy deletes ignored build output inside a
// repository: permanent deletion is only allowed when git shows that nothing
// untracked and unignored is lost.
func TestTrashApplyDeleteStrategy(t *testing.T) {
	fx, repo := forgedRepoFixture(t, false)
	fx.strategy = config.StrategyDelete
	p := filepath.Join(repo.Dir, "build", "out.bin")
	step, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(filepath.Join(repo.Dir, "build")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(step.Description, "permanently delete build") || !strings.HasPrefix(step.Command, displayVerb(config.StrategyDelete)) {
		t.Errorf("step = %+v", step)
	}
	en, err := trashAction{}.Apply(context.Background(), fx.env, step)
	if err != nil {
		t.Fatal(err)
	}
	if en.Restorable || en.RecoveryHint != "not recoverable" || en.Status != session.StatusApplied {
		t.Fatalf("entry = %+v", en)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still exists: %v", err)
	}
	// Undo of a permanent deletion passes ErrNotRestorable through.
	if err := (trashAction{}).Undo(context.Background(), fx.env, en); !errors.Is(err, trash.ErrNotRestorable) {
		t.Fatalf("Undo err = %v, want ErrNotRestorable", err)
	}
}

func TestTrashUndoErrors(t *testing.T) {
	ctx := context.Background()

	applied := func(t *testing.T, fx *trashFixture) (session.Entry, string) {
		p := fx.write("proj/a.log", "x")
		step, err := trashAction{}.Plan(ctx, fx.env, trashFinding(p))
		if err != nil {
			t.Fatal(err)
		}
		en, err := trashAction{}.Apply(ctx, fx.env, step)
		if err != nil {
			t.Fatal(err)
		}
		return en, p
	}

	t.Run("conflict", func(t *testing.T) {
		fx := newTrashFixture(t)
		en, p := applied(t, fx)
		fx.write("proj/a.log", "new")
		if err := (trashAction{}).Undo(ctx, fx.env, en); !errors.Is(err, trash.ErrRestoreConflict) {
			t.Fatalf("err = %v, want ErrRestoreConflict", err)
		}
		if b, _ := os.ReadFile(p); string(b) != "new" {
			t.Error("conflicting file was overwritten")
		}
	})
	t.Run("not restorable", func(t *testing.T) {
		fx := newTrashFixture(t)
		en, _ := applied(t, fx)
		if err := os.RemoveAll(filepath.Dir(en.Trash.StoredPath)); err != nil {
			t.Fatal(err)
		}
		if err := (trashAction{}).Undo(ctx, fx.env, en); !errors.Is(err, trash.ErrNotRestorable) {
			t.Fatalf("err = %v, want ErrNotRestorable", err)
		}
	})
	t.Run("recorded strategy wins over configured one", func(t *testing.T) {
		fx := newTrashFixture(t)
		en, p := applied(t, fx)
		fx.strategy = config.StrategyDelete
		if err := (trashAction{}).Undo(ctx, fx.env, en); err != nil {
			t.Fatalf("Undo: %v", err)
		}
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("not restored: %v", err)
		}
	})
	t.Run("forged traversal", func(t *testing.T) {
		fx := newTrashFixture(t)
		stub := &stubTrasher{strategy: config.StrategyQuarantine}
		fx.useStub(stub)
		outside := filepath.Join(testutil.ResolvedTempDir(t), "evil")
		for _, orig := range []string{
			outside,
			filepath.Join(fx.root, "..", "evil"),
			filepath.Join(fx.root, "proj", "..", "..", "evil"),
			"relative/evil",
			"",
		} {
			en := session.Entry{Status: session.StatusApplied, Trash: &trash.Record{
				Strategy: config.StrategyQuarantine, OriginalPath: orig, StoredPath: "/q/1/evil", Restorable: true,
			}}
			if err := (trashAction{}).Undo(ctx, fx.env, en); err == nil {
				t.Errorf("Undo(%q) succeeded, want refusal", orig)
			}
		}
		if len(stub.restored) != 0 {
			t.Errorf("Restore called for forged entries: %v", stub.restored)
		}
	})
	t.Run("invalid entries", func(t *testing.T) {
		fx := newTrashFixture(t)
		rec := &trash.Record{Strategy: config.StrategyQuarantine, OriginalPath: fx.path("a"), Restorable: true}
		for name, en := range map[string]session.Entry{
			"no record":       {Status: session.StatusApplied},
			"failed status":   {Status: session.StatusFailed, Trash: rec},
			"restored status": {Status: session.StatusRestored, Trash: rec},
		} {
			if err := (trashAction{}).Undo(ctx, fx.env, en); err == nil {
				t.Errorf("%s: Undo succeeded", name)
			}
		}
		fx.env.TrasherFor = nil
		if err := (trashAction{}).Undo(ctx, fx.env, session.Entry{Status: session.StatusApplied, Trash: rec}); err == nil {
			t.Error("Undo without TrasherFor succeeded")
		}
	})
	t.Run("restore uses the resolved destination", func(t *testing.T) {
		fx := newTrashFixture(t)
		stub := &stubTrasher{strategy: config.StrategyQuarantine}
		fx.useStub(stub)
		en := session.Entry{Status: session.StatusApplied, Trash: &trash.Record{
			Strategy: config.StrategyQuarantine, OriginalPath: fx.path("proj/x.log"), Restorable: true,
		}}
		if err := (trashAction{}).Undo(ctx, fx.env, en); err != nil {
			t.Fatal(err)
		}
		if len(stub.restored) != 1 || stub.restored[0].OriginalPath != fx.path("proj/x.log") {
			t.Fatalf("restored = %+v", stub.restored)
		}
	})
}

func TestDescribeAndDisplayCommand(t *testing.T) {
	t.Setenv(config.HomeEnv, filepath.Join(t.TempDir(), "h"))
	tests := []struct {
		strategy config.TrashStrategy
		notes    []string
		wantDesc string
		wantCmd  string
	}{
		{config.StrategyTrash, nil, "move node_modules to trash", displayVerb(config.StrategyTrash)},
		{config.StrategyQuarantine, nil, "move node_modules to quarantine", displayVerb(config.StrategyQuarantine)},
		{config.StrategyDelete, nil, "permanently delete node_modules", displayVerb(config.StrategyDelete)},
		{config.StrategyTrash, []string{"tracked files", "open-file check incomplete"}, "move node_modules to trash [tracked files; open-file check incomplete]", displayVerb(config.StrategyTrash)},
	}
	for _, tt := range tests {
		t.Run(string(tt.strategy)+strings.Join(tt.notes, ","), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "node_modules")
			if got := describe(tt.strategy, path, tt.notes); got != tt.wantDesc {
				t.Errorf("describe = %q, want %q", got, tt.wantDesc)
			}
			if got := displayCommand(tt.strategy, path); !strings.HasPrefix(got, tt.wantCmd) || !strings.Contains(got, "node_modules") {
				t.Errorf("displayCommand = %q", got)
			}
		})
	}
}

func TestTrashViaExecutor(t *testing.T) {
	fx := newTrashFixture(t)
	p := fx.write("proj/build/out.bin", "12345")
	fx.write("proj/keep.txt", "keep")
	dirs, err := config.EnsureDirs()
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(dirs.Sessions)
	var out strings.Builder
	ex := NewExecutor(Options{
		Apply: true, Yes: true, Env: fx.env, Store: store, SessionID: "20260930-120000-test",
		Command: "brooom sweep", IO: IO{Out: &out, Err: &out},
		StdinIsTTY: func() bool { return false },
	})
	res, err := ex.Run(context.Background(), []findings.Finding{trashFinding(filepath.Dir(p))})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if res.Applied != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v\n%s", res, out.String())
	}
	if _, err := os.Lstat(filepath.Dir(p)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("directory still exists: %v", err)
	}
	if _, err := os.Stat(fx.path("proj/keep.txt")); err != nil {
		t.Errorf("sibling was removed: %v", err)
	}
}
