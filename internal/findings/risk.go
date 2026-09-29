package findings

// RiskFlag names a reason to be careful before acting on a finding.
//
// Blocking flags mean acting on the finding could lose work that exists
// nowhere else (or break a running process). A detector that sets a blocking
// flag must set SuggestedAction.Type to ActionNone; actions refuse to act on
// findings with blocking flags unless the user passes --force. Informational
// flags only add context.
type RiskFlag string

const (
	// RiskUnpushedCommits: the branch has commits that exist on no remote.
	RiskUnpushedCommits RiskFlag = "unpushed_commits"
	// RiskUncommittedChanges: the path contains modified or untracked files
	// that are not ignored.
	RiskUncommittedChanges RiskFlag = "uncommitted_changes"
	// RiskFileOpen: a process currently has the file (or a file below the
	// directory) open, e.g. a log still being written.
	RiskFileOpen RiskFlag = "file_open_by_process"
	// RiskWorktreeDirty: the worktree has uncommitted or untracked changes.
	RiskWorktreeDirty RiskFlag = "worktree_dirty"
	// RiskWorktreeLocked: the worktree is locked (git worktree lock).
	RiskWorktreeLocked RiskFlag = "worktree_locked"
	// RiskHasOpenPR: an open pull request uses the branch (via gh, optional).
	RiskHasOpenPR RiskFlag = "has_open_pr"
	// RiskCurrentBranch: the branch is checked out in some worktree.
	RiskCurrentBranch RiskFlag = "current_branch"
	// RiskProtectedBranch: the branch matches a protected pattern (main,
	// master, develop, release/*, configured patterns).
	RiskProtectedBranch RiskFlag = "protected_branch"
	// RiskTrackedFiles: the path contains files tracked by git; removing it
	// would show up as a deletion in the working tree.
	RiskTrackedFiles RiskFlag = "tracked_files"
	// RiskRecentlyModified: the path changed within the configured "recent"
	// window, so it may still be in use.
	RiskRecentlyModified RiskFlag = "recently_modified"

	// RiskGitignored: the path is ignored by git (informational; usually
	// makes removal safer, not riskier).
	RiskGitignored RiskFlag = "gitignored"
	// RiskNeverPushed: the branch was never pushed to any remote.
	RiskNeverPushed RiskFlag = "never_pushed"
	// RiskUpstreamGone: the branch's upstream tracking branch no longer exists.
	RiskUpstreamGone RiskFlag = "upstream_gone"
	// RiskSymlink: the path is (or contains at its top level) a symlink; only
	// the link itself is ever removed, never its target.
	RiskSymlink RiskFlag = "symlink"
	// RiskOutsideRepo: the finding is a user-level location outside any repo.
	RiskOutsideRepo RiskFlag = "outside_repo"
)

// blockingRisks are the flags that prevent an action unless forced.
var blockingRisks = map[RiskFlag]bool{
	RiskUnpushedCommits:    true,
	RiskUncommittedChanges: true,
	RiskFileOpen:           true,
	RiskWorktreeDirty:      true,
	RiskWorktreeLocked:     true,
	RiskHasOpenPR:          true,
	RiskCurrentBranch:      true,
	RiskProtectedBranch:    true,
	RiskTrackedFiles:       true,
}

// neverOverridable are blocking flags that --force does not override: acting
// on them would break a checkout, a running process or a lock the user set on
// purpose.
var neverOverridable = map[RiskFlag]bool{
	RiskFileOpen:        true,
	RiskWorktreeLocked:  true,
	RiskCurrentBranch:   true,
	RiskProtectedBranch: true,
}

// Blocking reports whether the flag prevents acting on a finding unless the
// user explicitly forces it.
func (r RiskFlag) Blocking() bool {
	return blockingRisks[r]
}

// ForceOverridable reports whether --force allows acting despite this flag.
// Informational flags are trivially overridable; file_open_by_process,
// worktree_locked, current_branch and protected_branch never are.
func (r RiskFlag) ForceOverridable() bool {
	return !neverOverridable[r]
}

// Actionable reports whether an action may run on a finding with these
// flags: always when no flag is blocking, with force only when every blocking
// flag is overridable.
func Actionable(flags []RiskFlag, force bool) bool {
	for _, r := range flags {
		if r.Blocking() && (!force || !r.ForceOverridable()) {
			return false
		}
	}
	return true
}

// AllRiskFlags returns every known risk flag in a stable order. Used for
// documentation, validation and completions.
func AllRiskFlags() []RiskFlag {
	return []RiskFlag{
		RiskUnpushedCommits, RiskUncommittedChanges, RiskFileOpen,
		RiskWorktreeDirty, RiskWorktreeLocked, RiskHasOpenPR,
		RiskCurrentBranch, RiskProtectedBranch, RiskTrackedFiles,
		RiskRecentlyModified, RiskGitignored, RiskNeverPushed,
		RiskUpstreamGone, RiskSymlink, RiskOutsideRepo,
	}
}
