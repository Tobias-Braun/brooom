package worktrees

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// evBranchRefMissing marks a worktree whose checked-out branch has no ref.
const evBranchRefMissing = "branch_ref_missing"

// isZeroSHA reports the all-zero object id that `git worktree list` prints
// as HEAD when the branch it points to does not resolve.
func isZeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

// branchRefUnresolved reports a worktree on a branch whose ref does not
// resolve to a commit: the ref was deleted behind git's back
// (`git update-ref -d`, `git branch -D` refuses checked-out branches) or the
// branch is unborn (`git worktree add --orphan`).
func branchRefUnresolved(e *entry) bool {
	wt := e.wt
	return !wt.Detached && wt.Branch != "" && (wt.Head == "" || isZeroSHA(wt.Head))
}

// lastLoggedHead returns the newest commit the worktree's HEAD reflog knows,
// "" when there is none. The reflog lives in the worktree's admin directory
// and outlives a deleted branch ref. found tells whether a reflog exists at
// all: a worktree that never had a commit (unborn) has none.
func (s *scan) lastLoggedHead(e *entry) (sha string, found bool) {
	admin, ok := gitx.WorktreeAdminDir(s.repo.Common, e.wt.Path)
	if !ok {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(admin, "logs", "HEAD"))
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) >= 2 && !isZeroSHA(fields[1]) {
		return fields[1], true
	}
	return "", true
}

// missingRefRule reports a worktree whose branch ref is gone. The commits of
// the worktree may exist only in reflogs, so this never suggests an action:
// the finding explains how to restore the ref instead. An unborn orphan
// branch is normal for a worktree that has not committed yet and stays quiet
// (ok false, but handled so no other rule misreads the zero HEAD).
//
// The two cases are told apart by a heuristic: a deleted ref leaves a HEAD
// reflog in the worktree's admin directory (the worktree once had a commit),
// an unborn branch has none. If that reflog is missing or unreadable for a
// deleted ref (for example after manual cleanup), the worktree is treated as
// unborn and stays quiet, the quiet side of a report-only rule.
func (s *scan) missingRefRule(e *entry) (v verdict, ok, handled bool) {
	if !branchRefUnresolved(e) {
		return verdict{}, false, false
	}
	sha, hadLog := s.lastLoggedHead(e)
	if !hadLog {
		return verdict{}, false, true
	}
	if sha == "" {
		sha = "<sha>"
	}
	b := e.wt.Branch
	return verdict{
		conf:   findings.ConfidenceLow,
		action: findings.ActionNone,
		reason: "the branch ref is missing and its commits may exist only in reflogs; restore it with git branch " + b + " " + sha +
			" (from the worktree's reflog) or run git worktree repair",
		evidence: []findings.Evidence{{
			Code: evBranchRefMissing, Message: "branch " + b + " is checked out here but its ref does not exist", Value: e.wt.BranchRef,
		}},
	}, true, true
}
