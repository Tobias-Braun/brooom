package gitx

import (
	"os"
	"path/filepath"
	"strings"
)

// OperationBranch returns the short name of the branch that a rebase or
// bisect in progress in gitDir will return to, or "" when none is recorded
// (a rebase of a detached HEAD, other operations, unreadable files). While
// such an operation runs HEAD is detached, so neither `git worktree list` nor
// for-each-ref's %(worktreepath) shows that the branch is in use; the branch
// still cannot be deleted and its worktree must not be removed.
func OperationBranch(gitDir string) string {
	for _, name := range []string{"rebase-merge/head-name", "rebase-apply/head-name"} {
		if ref := readFirstLine(filepath.Join(gitDir, filepath.FromSlash(name))); strings.HasPrefix(ref, "refs/heads/") {
			return strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	// BISECT_START holds the branch name, or a commit id when the bisect
	// started on a detached HEAD, which never equals a branch name.
	return readFirstLine(filepath.Join(gitDir, "BISECT_START"))
}

// readFirstLine returns the first line of a small file without its line
// ending, or "" when the file cannot be read.
func readFirstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

// WorktreeAdminDir returns the private git directory below
// <common>/worktrees that belongs to the linked worktree at path, found via
// the "gitdir" back-pointer each entry keeps (it names <worktree>/.git). The
// directory name cannot be derived from the path because git adds numeric
// suffixes on collisions. ok is false when no entry matches.
func WorktreeAdminDir(common, path string) (dir string, ok bool) {
	if common == "" {
		return "", false
	}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		admin := filepath.Join(common, "worktrees", e.Name())
		ptr := readFirstLine(filepath.Join(admin, "gitdir"))
		if ptr != "" && SamePath(filepath.Dir(cleanNative(ptr)), path) {
			return admin, true
		}
	}
	return "", false
}

// lockReasonUnknown is the reason of a worktree whose lock state could not
// be read on a git that does not report it.
const lockReasonUnknown = "lock state could not be determined (git older than 2.31)"

// annotateWorktrees adds what `git worktree list` does not tell, all from
// read-only file system access to the git administration:
//
//   - Locked and LockReason on git older than 2.31, which has no "locked"
//     token. The lock is the file <admin>/locked. When the administrative
//     directory cannot be found the worktree is treated as locked, because
//     unknown must never read as unlocked.
//   - Operation and OperationBranch: a rebase, merge, cherry-pick, revert or
//     bisect in progress in the worktree.
//   - HasSubmodules: initialized submodules, which make `git worktree remove`
//     refuse.
func (r *Repo) annotateWorktrees(wts []Worktree, v Version) {
	for i := range wts {
		w := &wts[i]
		if w.Bare {
			continue
		}
		dir, ok := r.Common, r.Common != ""
		if !w.Main {
			dir, ok = WorktreeAdminDir(r.Common, w.Path)
		}
		if !v.AtLeast(2, 31) && !w.Main && !w.Locked {
			w.Locked, w.LockReason = legacyLock(dir, ok)
		}
		if !ok {
			continue
		}
		if op, _, found := OperationInProgress(dir); found {
			w.Operation, w.OperationBranch = op, OperationBranch(dir)
		}
		if !w.Main {
			w.HasSubmodules = hasEntries(filepath.Join(dir, "modules"))
		}
	}
}

// legacyLock reads the lock of a linked worktree the way git >= 2.31 reports
// it. An unknown administrative directory fails closed.
func legacyLock(adminDir string, found bool) (locked bool, reason string) {
	if !found {
		return true, lockReasonUnknown
	}
	b, err := os.ReadFile(filepath.Join(adminDir, "locked"))
	switch {
	case err == nil:
		line, _, _ := strings.Cut(string(b), "\n")
		return true, strings.TrimSpace(line)
	case os.IsNotExist(err):
		return false, ""
	default:
		return true, lockReasonUnknown
	}
}

// hasEntries reports whether dir is a directory with at least one entry. Git
// keeps the object store of every initialized submodule of a linked worktree
// in <admin>/modules; a leftover directory of a de-initialized submodule
// counts too, which errs on the side of not removing.
func hasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}
