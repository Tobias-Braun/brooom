package gitx_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// wtView is the comparable projection of a Worktree the tests assert on.
type wtView struct {
	Path                                 string
	Main, Locked, Prunable, Missing, Det bool
	Reason                               string
}

func view(w gitx.Worktree) wtView {
	return wtView{w.Path, w.Main, w.Locked, w.Prunable, w.DirMissing, w.Detached, w.LockReason}
}

func TestListWorktrees(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	requireGit(t, r, 2, 31)
	repo := testutil.NewRepo(t)
	linked := repo.AddWorktree("linked", "feat")
	locked := repo.AddWorktree("locked", "locked-reason")
	repo.Git("worktree", "lock", "--reason", "on usb stick", locked)
	plain := repo.AddWorktree("locked-plain", "locked-plain")
	repo.Git("worktree", "lock", plain)
	detached := repo.AddWorktree("detached", "")
	gone := repo.AddWorktree("gone", "gone-branch")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	wts, err := openRepo(t, r, repo.Dir).ListWorktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]wtView{
		"main":          {Path: repo.Dir, Main: true},
		"feat":          {Path: linked},
		"locked-reason": {Path: locked, Locked: true, Reason: "on usb stick"},
		"locked-plain":  {Path: plain, Locked: true},
		"gone-branch":   {Path: gone, Prunable: true, Missing: true},
		"":              {Path: detached, Det: true},
	}
	if len(wts) != len(want) {
		t.Fatalf("got %d worktrees, want %d: %+v", len(wts), len(want), wts)
	}
	for _, w := range wts {
		if got := view(w); got != want[w.Branch] {
			t.Errorf("worktree %q = %+v, want %+v", w.Branch, got, want[w.Branch])
		}
		if w.Head == "" {
			t.Errorf("worktree %q has no HEAD", w.Branch)
		}
	}
}

func TestListWorktreesBareRepository(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	requireGit(t, r, 2, 23)
	repo := testutil.NewRepoWithRemote(t)
	wt := filepath.Join(testutil.ResolvedTempDir(t), "from-bare")
	repo.Git("-C", repo.Origin, "worktree", "add", "-q", "--detach", wt)

	handle := openRepo(t, r, wt)
	wts, err := handle.ListWorktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 2 || !wts[0].Bare || !wts[0].Main || wts[1].Path != wt {
		t.Fatalf("worktrees = %+v", wts)
	}
	if _, err := handle.MainWorktree(ctx); !errors.Is(err, gitx.ErrBareRepo) {
		t.Errorf("MainWorktree err = %v, want ErrBareRepo", err)
	}
}

// TestListWorktreesLegacyGit runs the fallbacks used by git older than 2.31
// (no "prunable") and 2.36 (no -z) against canned newline porcelain.
func TestListWorktreesLegacyGit(t *testing.T) {
	missing := filepath.Join(testutil.ResolvedTempDir(t), "missing")
	existing := testutil.ResolvedTempDir(t)
	porcelain := "worktree " + existing + "\nHEAD aaaa\nbranch refs/heads/main\n\n" +
		"worktree " + missing + "\r\nHEAD bbbb\r\nbranch refs/heads/old\r\n\r\n" +
		"worktree " + missing + "-locked\nHEAD cccc\ndetached\nlocked\n"
	var sawZ bool
	fake := fakeRunner(func(args []string) (string, error) {
		switch args[0] {
		case "version":
			return "git version 2.30.2", nil
		case "worktree":
			for _, a := range args {
				sawZ = sawZ || a == "-z"
			}
			return porcelain, nil
		}
		return "", errors.New("unexpected " + args[0])
	})
	// Git < 2.31 has no "locked" token, so the administrative directories
	// must exist for the lock fallback.
	common := testutil.ResolvedTempDir(t)
	addAdmin(t, common, "old", missing)
	addAdmin(t, common, "old-locked", missing+"-locked")
	repo := &gitx.Repo{Runner: fake, Dir: ".", Common: common}
	wts, err := repo.ListWorktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sawZ {
		t.Error("git < 2.36 must not be given -z")
	}
	if len(wts) != 3 {
		t.Fatalf("worktrees = %+v", wts)
	}
	if wts[0].Prunable || !wts[0].Main {
		t.Errorf("existing main: %+v", wts[0])
	}
	if !wts[1].DirMissing || !wts[1].Prunable || wts[1].Branch != "old" {
		t.Errorf("missing dir must be prunable via fallback: %+v", wts[1])
	}
	if !wts[2].DirMissing || wts[2].Prunable || !wts[2].Locked || !wts[2].Detached {
		t.Errorf("locked missing dir must not be prunable: %+v", wts[2])
	}
}

func TestParseWorktreesZFormat(t *testing.T) {
	out := "worktree /a\x00HEAD 1\x00branch refs/heads/x\x00\x00worktree /b\x00HEAD 2\x00detached\x00prunable gitdir file points to non-existent location\x00\x00"
	wts := gitx.ParseWorktrees(out, "\x00")
	if len(wts) != 2 || wts[0].Branch != "x" || !wts[1].Prunable || wts[1].PruneReason == "" {
		t.Errorf("parsed %+v", wts)
	}
}

func TestIsDirty(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	tests := []struct {
		name  string
		setup func(repo *testutil.Repo)
		want  bool
	}{
		{"clean", func(*testutil.Repo) {}, false},
		{"modified", func(repo *testutil.Repo) { repo.WriteFile("README.md", "changed\n") }, true},
		{"staged", func(repo *testutil.Repo) {
			repo.WriteFile("new.txt", "n")
			repo.Git("add", "new.txt")
		}, true},
		{"deleted", func(repo *testutil.Repo) { _ = os.Remove(filepath.Join(repo.Dir, "README.md")) }, true},
		{"untracked", func(repo *testutil.Repo) { repo.WriteFile("untracked.txt", "u") }, true},
		{"ignored only", func(repo *testutil.Repo) {
			repo.Commit(".gitignore", "*.log\n", "ignore logs", at(1))
			repo.WriteFile("debug.log", "noise")
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			tc.setup(repo)
			got, err := openRepo(t, r, repo.Dir).IsDirty(ctx, repo.Dir)
			if err != nil || got != tc.want {
				t.Errorf("IsDirty = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// TestIsDirtyLeavesIndexUntouched makes a file stat-dirty without changing its
// content, the situation in which a plain `git status` would refresh and
// rewrite the index, and asserts the index file stays identical.
func TestIsDirtyLeavesIndexUntouched(t *testing.T) {
	repo := testutil.NewRepo(t)
	readme := filepath.Join(repo.Dir, "README.md")
	testutil.SetMTime(t, readme, testutil.BaseTime.AddDate(1, 0, 0))
	index := filepath.Join(repo.Dir, ".git", "index")
	beforeBytes, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, _ := os.Stat(index)

	dirty, err := openRepo(t, execRunner(t), repo.Dir).IsDirty(context.Background(), repo.Dir)
	if err != nil || dirty {
		t.Fatalf("IsDirty = %v, %v; want clean", dirty, err)
	}
	afterBytes, _ := os.ReadFile(index)
	afterStat, _ := os.Stat(index)
	if !bytes.Equal(beforeBytes, afterBytes) || !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Error(".git/index was modified by IsDirty")
	}
}
