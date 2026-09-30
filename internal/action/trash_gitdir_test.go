package action

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// bareLayout builds the "proj/.bare + proj/.git file" layout of a bare clone
// with worktrees: none of the metadata lives in a directory named .git.
func bareLayout(t *testing.T, fx *trashFixture) (proj string) {
	t.Helper()
	proj = fx.mkdir("proj")
	cmd := exec.Command("git", "init", "--bare", "-q", filepath.Join(proj, ".bare"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init --bare failed: %v: %s", err, out)
	}
	fx.write("proj/.git", "gitdir: ./.bare\n")
	return proj
}

// TestTrashRefusesGitDirsNotNamedGit is the regression for a metadata
// directory with an arbitrary name: --force must never lift the refusal.
func TestTrashRefusesGitDirsNotNamedGit(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, rel := range []string{"objects", "refs", "hooks", "objects/pack", "."} {
			name := "force=off/" + rel
			if force {
				name = "force=on/" + rel
			}
			t.Run(name, func(t *testing.T) {
				fx := newTrashFixture(t)
				proj := bareLayout(t, fx)
				fx.env.Force = force
				target := filepath.Join(proj, ".bare", filepath.FromSlash(rel))
				_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(target))
				wantSkip(t, err, "git")
			})
		}
	}
}

// TestTrashRefusesSeparateGitDir covers --separate-git-dir: the git directory
// has an arbitrary name and is only known from the .git file.
func TestTrashRefusesSeparateGitDir(t *testing.T) {
	fx := newTrashFixture(t)
	fx.env.Force = true
	work := fx.mkdir("work")
	gitdir := fx.path("meta/store")
	cmd := exec.Command("git", "init", "-q", "--separate-git-dir", gitdir, work)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init --separate-git-dir failed: %v: %s", err, out)
	}
	for _, rel := range []string{"objects", "hooks", "refs/heads"} {
		_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(filepath.Join(gitdir, rel)))
		wantSkip(t, err, "git")
	}
}

// TestTrashRefusesLinkedGitDirWithoutBareShape covers a git directory that
// does not look bare (no HEAD/objects/refs) and is only known from the .git
// link file of an ancestor.
func TestTrashRefusesLinkedGitDirWithoutBareShape(t *testing.T) {
	fx := newTrashFixture(t)
	fx.env.Force = true
	fx.write("proj/.git", "gitdir: ./store\n")
	fx.write("proj/store/cache/x", "x")
	_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(fx.path("proj/store/cache")))
	wantSkip(t, err, "git directory")
}

// TestTrashRefusesCommonDirOfWorktree covers a link to a linked worktree's
// git dir (store/worktrees/wt), whose "commondir" names the shared store.
func TestTrashRefusesCommonDirOfWorktree(t *testing.T) {
	fx := newTrashFixture(t)
	fx.env.Force = true
	fx.write("proj/.git", "gitdir: ./store/worktrees/wt\n")
	fx.write("proj/store/worktrees/wt/commondir", "../..\n")
	fx.write("proj/store/shared/x", "x")
	_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(fx.path("proj/store/shared")))
	wantSkip(t, err, "git directory")
}

// TestGitDirRefusalSparesWorkTree makes sure the new ancestor check does not
// reach the working tree that merely owns a .git link file.
func TestGitDirRefusalSparesWorkTree(t *testing.T) {
	fx := newTrashFixture(t)
	gitdir := fx.mkdir("meta/store")
	fx.mkdir("work/build")
	fx.write("work/.git", "gitdir: "+gitdir+"\n")
	if err := refuseGitDir(fx.env, fx.path("work/build")); err != nil {
		t.Fatalf("work tree content refused: %v", err)
	}
}

// TestCheckTrackedOutsideRepositoryIsHardRefusal: git saying the path is
// outside the repository must skip even with --force.
func TestCheckTrackedOutsideRepositoryIsHardRefusal(t *testing.T) {
	fx := newTrashFixture(t)
	fx.env.Force = true
	fx.mkdir("repo/.git")
	target := fx.write("repo/a.txt", "x")
	fx.env.Git = lsFilesFailRunner{msg: "fatal: '" + target + "' is outside repository at '/x'"}
	_, err := checkTracked(context.Background(), fx.env, target)
	wantSkip(t, err, "outside")
}

// lsFilesFailRunner fails every git call with a fixed message.
type lsFilesFailRunner struct {
	gitx.Runner
	msg string
}

func (f lsFilesFailRunner) Run(context.Context, string, ...string) (string, error) {
	return "", errors.New(f.msg)
}
