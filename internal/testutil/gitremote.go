package testutil

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// NewRepoWithRemote creates a repository like NewRepo plus a bare origin in
// its own temporary directory. main is pushed with upstream tracking and
// origin/HEAD is set, so the repository looks like a normal clone.
func NewRepoWithRemote(t testing.TB) *Repo {
	t.Helper()
	r := NewRepo(t)
	r.Origin = ResolvedTempDir(t)
	r.Git("init", "--bare", "-q", "-b", "main", r.Origin)
	r.Git("remote", "add", "origin", r.Origin)
	r.Git("push", "-q", "-u", "origin", "main")
	r.Fetch()
	r.Git("remote", "set-head", "origin", "main")
	return r
}

// Commit writes a file and commits it on the current branch at when.
func (r *Repo) Commit(file, content, msg string, when time.Time) {
	r.t.Helper()
	r.WriteFile(file, content)
	r.CommitAll(msg, when)
}

// Push pushes a branch to origin and sets it as upstream.
func (r *Repo) Push(branch string) {
	r.t.Helper()
	r.Git("push", "-q", "-u", "origin", branch)
}

// Fetch fetches origin and prunes remote-tracking branches deleted there.
func (r *Repo) Fetch() {
	r.t.Helper()
	r.Git("fetch", "-q", "--prune", "origin")
}

// DeleteRemoteBranch deletes a branch on origin. The remote-tracking ref
// disappears with the next Fetch.
func (r *Repo) DeleteRemoteBranch(branch string) {
	r.t.Helper()
	r.Git("push", "-q", "origin", "--delete", branch)
}

// SquashMerge squash-merges branch into the current branch as one commit.
func (r *Repo) SquashMerge(branch, msg string, when time.Time) {
	r.t.Helper()
	r.GitAt(when, "merge", "--squash", branch)
	r.GitAt(when, "commit", "-q", "-m", msg)
}

// RebaseMerge replays every commit of branch onto the current branch with
// new committer dates (one second apart from when), like a rebase merge on a
// hosting platform. Empty commits are kept.
func (r *Repo) RebaseMerge(branch string, when time.Time) {
	r.t.Helper()
	list := r.Git("rev-list", "--reverse", "--topo-order", "HEAD.."+branch)
	for i, sha := range strings.Fields(list) {
		r.GitAt(when.Add(time.Duration(i)*time.Second), "cherry-pick", "--allow-empty", "--keep-redundant-commits", sha)
	}
}

// AddWorktree creates a linked worktree at rel below a per-repository
// temporary directory (outside the repository) and returns its resolved
// path. An existing branch is checked out; a missing one is created at HEAD;
// an empty branch creates a detached worktree.
func (r *Repo) AddWorktree(rel, branch string) string {
	r.t.Helper()
	if r.wtRoot == "" {
		r.wtRoot = ResolvedTempDir(r.t)
	}
	p := filepath.Join(r.wtRoot, rel)
	switch {
	case branch == "":
		r.Git("worktree", "add", "-q", "--detach", p)
	case r.branchExists(branch):
		r.Git("worktree", "add", "-q", p, branch)
	default:
		r.Git("worktree", "add", "-q", "-b", branch, p)
	}
	return p
}

func (r *Repo) branchExists(name string) bool {
	r.t.Helper()
	return r.Git("branch", "--list", name) != ""
}
