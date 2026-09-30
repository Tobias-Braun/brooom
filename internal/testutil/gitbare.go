package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// BareLayout is the "bare repository plus linked worktrees" layout many
// parallel-agent setups use: a bare clone in Root/.bare, a Root/.git file
// pointing at it, and one linked worktree per branch below Root.
type BareLayout struct {
	// Root is the project folder holding .bare, the .git pointer and the
	// worktrees.
	Root string
	// Bare is the bare repository, Root/.bare.
	Bare string
	// Main and Feat are the linked worktrees of the branches main and
	// feat/x, Root/main and Root/feat.
	Main, Feat string
	// Repo drives git commands; its Dir is a separate scratch clone that
	// shares the origin, useful to publish commits.
	Repo *Repo
}

// NewBareLayout builds the layout with git itself, exactly as users do:
// clone --bare, a ".git" file with "gitdir: ./.bare", then worktree add. The
// remote-tracking refspec is configured because a bare clone has none.
func NewBareLayout(t testing.TB) *BareLayout {
	t.Helper()
	src := NewRepoWithRemote(t)
	root := ResolvedTempDir(t)
	l := &BareLayout{
		Root: root, Bare: filepath.Join(root, ".bare"),
		Main: filepath.Join(root, "main"), Feat: filepath.Join(root, "feat"),
		Repo: src,
	}
	src.Git("clone", "-q", "--bare", src.Origin, l.Bare)
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ./.bare\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src.Git("-C", l.Bare, "config", "gc.auto", "0")
	src.Git("-C", l.Bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	src.Git("-C", l.Bare, "fetch", "-q", "origin")
	src.Git("-C", l.Bare, "remote", "set-head", "origin", "main")
	src.Git("-C", root, "worktree", "add", "-q", l.Main, "main")
	src.Git("-C", root, "worktree", "add", "-q", "-b", "feat/x", l.Feat)
	return l
}
