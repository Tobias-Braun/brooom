package gitx_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// addAdmin creates <common>/worktrees/<name> with the "gitdir" back-pointer
// git writes for a linked worktree at wt and returns the directory.
func addAdmin(t *testing.T, common, name, wt string) string {
	t.Helper()
	admin := filepath.Join(common, "worktrees", name)
	testutil.WriteFile(t, admin, "gitdir", filepath.Join(wt, ".git")+"\n")
	return admin
}

// versionRunner answers `git version` and the given worktree porcelain.
func versionRunner(version, porcelain string) fakeRunner {
	return func(args []string) (string, error) {
		switch args[0] {
		case "version":
			return "git version " + version, nil
		case "worktree":
			return porcelain, nil
		}
		return "", errors.New("unexpected " + args[0])
	}
}

// TestListWorktreesLegacyLock covers git 2.23-2.30, which has no "locked"
// token: the lock is read from <admin>/locked and an unknown state fails
// closed. Before the fix Locked was always false there.
func TestListWorktreesLegacyLock(t *testing.T) {
	base := testutil.ResolvedTempDir(t)
	common := filepath.Join(base, "common")
	pathOf := func(n string) string { return filepath.Join(base, n) }
	for _, n := range []string{"plain", "reason", "empty", "gone-plain", "gone-locked"} {
		addAdmin(t, common, n, pathOf(n))
	}
	for n, reason := range map[string]string{"reason": "on usb stick\nsecond line", "empty": "", "gone-locked": "external drive"} {
		testutil.WriteFile(t, filepath.Join(common, "worktrees", n), "locked", reason)
	}
	for _, n := range []string{"main", "plain", "reason", "empty"} {
		if err := os.MkdirAll(pathOf(n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	porcelain := "worktree " + pathOf("main") + "\nHEAD a\nbranch refs/heads/main\n\n"
	for _, n := range []string{"plain", "reason", "empty", "gone-plain", "gone-locked", "orphan"} {
		porcelain += "worktree " + pathOf(n) + "\nHEAD b\ndetached\n\n"
	}
	repo := &gitx.Repo{Runner: versionRunner("2.30.2", porcelain), Dir: ".", Common: common}
	wts, err := repo.ListWorktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		locked   bool
		reason   string
		prunable bool
	}
	tests := map[string]want{
		"main":        {},
		"plain":       {},
		"reason":      {locked: true, reason: "on usb stick"},
		"empty":       {locked: true},
		"gone-plain":  {prunable: true},
		"gone-locked": {locked: true, reason: "external drive"},
		// No administrative directory: the lock state is unknowable, so the
		// worktree counts as locked and is never prunable.
		"orphan": {locked: true, reason: "lock state could not be determined (git older than 2.31)"},
	}
	for _, w := range wts {
		name := filepath.Base(w.Path)
		got := want{w.Locked, w.LockReason, w.Prunable}
		if got != tests[name] {
			t.Errorf("%s = %+v, want %+v", name, got, tests[name])
		}
	}
}

// TestListWorktreesLegacyLockWithoutCommonDir pins the fail-closed path for a
// handle that does not know its common directory.
func TestListWorktreesLegacyLockWithoutCommonDir(t *testing.T) {
	p := testutil.ResolvedTempDir(t)
	porcelain := "worktree " + p + "\nHEAD a\nbranch refs/heads/main\n\nworktree " + p + "x\nHEAD b\ndetached\n"
	repo := &gitx.Repo{Runner: versionRunner("2.25.1", porcelain), Dir: "."}
	wts, err := repo.ListWorktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if wts[0].Locked || !wts[1].Locked {
		t.Errorf("main locked=%v (want false), linked locked=%v (want true)", wts[0].Locked, wts[1].Locked)
	}
}

// TestListWorktreesAnnotations covers operations in progress (per worktree,
// including the main one) and initialized submodules on git 2.31+, where
// the lock token is trusted and the lock file is not consulted.
func TestListWorktreesAnnotations(t *testing.T) {
	base := testutil.ResolvedTempDir(t)
	common := filepath.Join(base, "common")
	pathOf := func(n string) string { return filepath.Join(base, n) }
	rebase := addAdmin(t, common, "rebase", pathOf("rebase"))
	testutil.WriteFile(t, filepath.Join(rebase, "rebase-merge"), "head-name", "refs/heads/feat/x\n")
	bisect := addAdmin(t, common, "bisect", pathOf("bisect"))
	testutil.WriteFile(t, bisect, "BISECT_LOG", "log\n")
	testutil.WriteFile(t, bisect, "BISECT_START", "topic\n")
	sub := addAdmin(t, common, "sub", pathOf("sub"))
	testutil.WriteFile(t, filepath.Join(sub, "modules", "lib"), "HEAD", "ref\n")
	addAdmin(t, common, "quiet", pathOf("quiet"))
	if err := os.MkdirAll(filepath.Join(common, "worktrees", "quiet", "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, common, "MERGE_HEAD", "abc\n")
	testutil.WriteFile(t, filepath.Join(common, "worktrees", "quiet"), "locked", "ignored on new git")

	porcelain := "worktree " + pathOf("main") + "\nHEAD a\nbranch refs/heads/main\n\n"
	for _, n := range []string{"rebase", "bisect", "sub", "quiet"} {
		porcelain += "worktree " + pathOf(n) + "\nHEAD b\ndetached\n\n"
	}
	repo := &gitx.Repo{Runner: versionRunner("2.35.0", porcelain), Dir: ".", Common: common}
	wts, err := repo.ListWorktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		op, opBranch string
		sub, locked  bool
	}
	tests := map[string]want{
		"main":   {op: "merge"},
		"rebase": {op: "rebase", opBranch: "feat/x"},
		"bisect": {op: "bisect", opBranch: "topic"},
		"sub":    {sub: true},
		"quiet":  {},
	}
	for _, w := range wts {
		name := filepath.Base(w.Path)
		if got := (want{w.Operation, w.OperationBranch, w.HasSubmodules, w.Locked}); got != tests[name] {
			t.Errorf("%s = %+v, want %+v", name, got, tests[name])
		}
	}
}

// TestBranchesInRebaseAreCheckedOut reproduces a real paused rebase: HEAD is
// detached, so for-each-ref reports no worktree for the branch, yet the
// branch is in use and must be treated as checked out.
func TestBranchesInRebaseAreCheckedOut(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	requireGit(t, r, 2, 23)
	repo := testutil.NewRepo(t)
	repo.WriteFile("f.txt", "base\n")
	repo.CommitAll("base", at(0))
	wt := repo.AddWorktree("rebasing", "topic")
	commitFile(t, repo, wt, "f.txt", "topic\n")
	repo.WriteFile("f.txt", "main\n")
	repo.CommitAll("main change", at(2))
	cmd := exec.Command("git", "-C", wt, "rebase", "-q", repo.Head())
	cmd.Env = repo.Env(at(3))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected a rebase conflict, got success\n%s", out)
	}

	handle := openRepo(t, r, repo.Dir)
	wts, err := handle.ListWorktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := findWorktreeAt(wts, wt)
	if !ok {
		t.Fatalf("worktree %s not listed: %+v", wt, wts)
	}
	if w.Operation != "rebase" || w.OperationBranch != "topic" || !w.Detached {
		t.Errorf("paused worktree = %+v", w)
	}
	branches, err := handle.ListBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range branches {
		if b.Name == "topic" && !gitx.SamePath(b.WorktreePath, wt) {
			t.Errorf("topic WorktreePath = %q, want %q", b.WorktreePath, wt)
		}
	}
}

func findWorktreeAt(wts []gitx.Worktree, path string) (gitx.Worktree, bool) {
	for _, w := range wts {
		if gitx.SamePath(w.Path, path) {
			return w, true
		}
	}
	return gitx.Worktree{}, false
}

// commitFile writes and commits a file inside a linked worktree.
func commitFile(t *testing.T, repo *testutil.Repo, wt, name, content string) {
	t.Helper()
	testutil.WriteFile(t, wt, name, content)
	for _, args := range [][]string{{"add", "-A"}, {"-c", "commit.gpgsign=false", "commit", "-q", "-m", "commit " + name}} {
		cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
		cmd.Env = repo.Env(testutil.BaseTime)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

func TestOperationBranch(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"rebase-merge", map[string]string{"rebase-merge/head-name": "refs/heads/a/b\n"}, "a/b"},
		{"rebase-apply", map[string]string{"rebase-apply/head-name": "refs/heads/c\n"}, "c"},
		{"detached rebase", map[string]string{"rebase-merge/head-name": "detached HEAD\n"}, ""},
		{"bisect", map[string]string{"BISECT_START": "topic\n"}, "topic"},
		{"nothing", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := testutil.ResolvedTempDir(t)
			for name, content := range tt.files {
				testutil.WriteFile(t, dir, filepath.FromSlash(name), content)
			}
			if got := gitx.OperationBranch(dir); got != tt.want {
				t.Errorf("OperationBranch = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOperationInProgressSequencer(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sequencer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if op, _, ok := gitx.OperationInProgress(dir); !ok || op == "" {
		t.Errorf("sequencer must count as an operation, got %q %v", op, ok)
	}
}

// TestOpenEnforcesMinGitVersion covers the version gate of Open and Cache.
func TestOpenEnforcesMinGitVersion(t *testing.T) {
	top := testutil.ResolvedTempDir(t)
	tests := []struct {
		name    string
		version string
		wantErr bool
		tooOld  bool
	}{
		{"current", "git version 2.43.0", false, false},
		{"minimum", "git version 2.23.0", false, false},
		{"too old", "git version 2.22.5", true, true},
		{"much older", "git version 1.8.3.1", true, true},
		{"vendor suffix", "git version 2.39.5 (Apple Git-154)", false, false},
		{"unparseable", "garbage", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := fakeRunner(func(args []string) (string, error) {
				switch args[0] {
				case "version":
					return tt.version, nil
				case "rev-parse":
					if args[1] == "--show-toplevel" {
						return top, nil
					}
					return filepath.Join(top, ".git"), nil
				}
				return "", errors.New("unexpected " + args[0])
			})
			ctx := context.Background()
			_, errOpen := gitx.Open(ctx, fake, top)
			_, errCache := gitx.NewCache(fake).Repo(ctx, top)
			for _, err := range []error{errOpen, errCache} {
				if (err != nil) != tt.wantErr {
					t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
				}
				if tt.wantErr && errors.Is(err, gitx.ErrGitTooOld) != tt.tooOld {
					t.Errorf("ErrGitTooOld match = %v, want %v (%v)", !tt.tooOld, tt.tooOld, err)
				}
			}
		})
	}
}

// TestOpenFailsWhenVersionCommandFails: an unknown version must not pass.
func TestOpenFailsWhenVersionCommandFails(t *testing.T) {
	top := testutil.ResolvedTempDir(t)
	fake := fakeRunner(func(args []string) (string, error) {
		switch args[0] {
		case "version":
			return "", errors.New("boom")
		case "rev-parse":
			if args[1] == "--show-toplevel" {
				return top, nil
			}
			return filepath.Join(top, ".git"), nil
		}
		return "", errors.New("unexpected " + args[0])
	})
	if _, err := gitx.Open(context.Background(), fake, top); err == nil {
		t.Fatal("expected an error for an undeterminable git version")
	}
}
