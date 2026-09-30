// Package scope decides what Brooom is allowed to look at and touch.
//
// By default Brooom operates only on the git repository containing the
// current directory (FindRepoRoot). With --workspaces it operates on every
// repository and project folder below the configured workspace roots
// (Discover). Every path a detector reports and every path an action touches
// must be validated through a Guard: the path is made absolute, symlinks are
// resolved and the result must lie inside one of the allowed locations.
// Anything outside is refused with ErrOutsideScope.
//
// # Known limitation (TOCTOU)
//
// Guard.Resolve is a check at call time. Between the check and the use of the
// returned path another process can swap a directory for a symlink. Actions
// must therefore call Resolve (or ResolveParent) again immediately before they
// touch anything and must use Guard.IsAllowedRoot to refuse removing an
// allowed location itself. Closing the window completely (openat or
// handle-based removal) is not attempted.
//
// # Refusal by default
//
// Errors other than ErrOutsideScope (symlink loops, permission errors,
// malformed paths) are refusals too. They stay distinct so messages are
// accurate, but callers must never fall back to "allow" on any error.
package scope

import (
	"errors"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// ErrOutsideScope is returned when a path resolves to a location outside
// every allowed root.
var ErrOutsideScope = errors.New("path is outside the allowed scope")

// ErrNotInRepo is returned by FindRepoRoot when no enclosing git repository
// exists.
var ErrNotInRepo = errors.New("not inside a git repository")

// TargetKind says what kind of folder a target is.
type TargetKind string

const (
	// TargetRepo is a git repository (or linked worktree) top-level directory.
	TargetRepo TargetKind = "repo"
	// TargetProject is a non-git project folder (has a project marker such
	// as package.json, go.mod, Cargo.toml, pyproject.toml).
	TargetProject TargetKind = "project"
	// TargetUser is a well-known user-level tool location from the catalog.
	TargetUser TargetKind = "user"
)

// Target is one unit of work for the detectors: a repo, a project folder or
// a user-level location.
type Target struct {
	Kind TargetKind
	// Path is the absolute, symlink-resolved directory of the target.
	Path string
	// Scope is the scope findings of this target are reported under: the repo
	// itself in repo mode, the workspace root in --workspaces mode, or the
	// user location.
	Scope findings.Scope
	// Tool is set for TargetUser: the catalog tool the location belongs to.
	Tool string
}
