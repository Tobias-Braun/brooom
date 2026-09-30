package scope

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// bareAnchor lays out proj/.bare (a bare repository) with a proj/.git file
// pointing at it, without needing git: discovery only reads marker files.
func bareAnchor(t *testing.T, root, rel, config string) string {
	t.Helper()
	proj := mkRel(t, root, rel)
	testutil.WriteFile(t, proj, ".bare/HEAD", "ref: refs/heads/main\n")
	testutil.WriteFile(t, proj, ".bare/config", config)
	testutil.WriteFile(t, proj, ".git", "gitdir: ./.bare\n")
	return proj
}

const bareConfig = "[core]\n\trepositoryformatversion = 0\n\tbare = true\n"

// linkedWorktree adds a linked worktree whose .git file points into the bare
// repository's worktrees directory.
func linkedWorktree(t *testing.T, proj, name string) {
	t.Helper()
	testutil.WriteFile(t, proj, name+"/.git", "gitdir: "+filepath.ToSlash(filepath.Join(proj, ".bare", "worktrees", name))+"\n")
	testutil.WriteFile(t, proj, name+"/main.go", "package x\n")
}

// TestDiscoverBareAnchor: a ".git" file that points at a bare repository is
// an anchor without a working tree, not a repository target; discovery keeps
// looking below it and reports the linked worktrees instead.
func TestDiscoverBareAnchor(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	proj := bareAnchor(t, root, "proj", bareConfig)
	linkedWorktree(t, proj, "main")
	linkedWorktree(t, proj, "feat")
	// A sibling ordinary repository must keep working next to the layout.
	fakeRepo(t, root, "plain")

	have := discoverOK(t, root, DiscoverOptions{})
	expect(t, have, repo("plain"), repo("proj/feat"), repo("proj/main"))
}

// TestDiscoverGitFileToNonBare: a ".git" file (submodule or --separate-git-dir
// layout) whose git directory is not bare stays an ordinary repository.
func TestDiscoverGitFileToNonBare(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	proj := bareAnchor(t, root, "proj", "[core]\n\tbare = false\n")
	testutil.WriteFile(t, proj, "inner/.git", "gitdir: ../.bare\n")

	expect(t, discoverOK(t, root, DiscoverOptions{}), repo("proj"))
}

// TestDiscoverBareAnchorUnreadable: a dangling pointer or a config without a
// bare flag never makes the folder skip its children silently; it is a
// repository as before.
func TestDiscoverBareAnchorUnreadable(t *testing.T) {
	isolateHome(t)
	root := testutil.ResolvedTempDir(t)
	proj := mkRel(t, root, "dangling")
	testutil.WriteFile(t, proj, ".git", "gitdir: ./missing\n")

	expect(t, discoverOK(t, root, DiscoverOptions{}), repo("dangling"))
	if _, err := os.Stat(filepath.Join(proj, ".git")); err != nil {
		t.Fatal(err)
	}
}
