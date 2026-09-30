package gitx

import (
	"context"
	"os"
	"path/filepath"
)

// GitDir returns the clean absolute git directory of the worktree containing
// dir. For a linked worktree that is its private directory below
// <common>/worktrees, for the main worktree it equals the common directory.
func GitDir(ctx context.Context, r Runner, dir string) (string, error) {
	out, err := r.Run(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", mapNotRepo(err)
	}
	return NormalizePath(out), nil
}

// operationMarkers are the entries git creates in a git directory while a
// multi-step operation is in progress: an interactive or apply-style rebase,
// a conflicted merge, cherry-pick or revert, and a bisect session.
var operationMarkers = []struct {
	name string
	op   string
}{
	{"rebase-merge", "rebase"},
	{"rebase-apply", "rebase or am"},
	{"MERGE_HEAD", "merge"},
	{"CHERRY_PICK_HEAD", "cherry-pick"},
	{"REVERT_HEAD", "revert"},
	{"BISECT_LOG", "bisect"},
}

// OperationInProgress reports the first operation (rebase, merge,
// cherry-pick, revert, bisect) that is in progress in any of the given git
// directories, with the directory it was found in. It only stats files, so it
// never takes a lock. Expiring or pruning while an operation runs can drop
// objects the operation still needs, which is why callers pass the git
// directories of every worktree of a repository (see WorktreeGitDirs).
func OperationInProgress(gitDirs ...string) (op, dir string, found bool) {
	for _, d := range gitDirs {
		for _, m := range operationMarkers {
			if _, err := os.Lstat(filepath.Join(d, m.name)); err == nil {
				return m.op, d, true
			}
		}
	}
	return "", "", false
}

// WorktreeGitDirs lists the common git directory plus the private git
// directory of every linked worktree registered below it. The list is read
// from the file system, not from git, so a broken registration cannot hide an
// operation in progress.
func WorktreeGitDirs(common string) []string {
	dirs := []string{common}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return dirs
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(common, "worktrees", e.Name()))
		}
	}
	return dirs
}
