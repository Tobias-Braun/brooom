// Package scope decides what Brooom is allowed to look at and touch.
//
// By default Brooom operates only on the git repository containing the
// current directory (FindRepoRoot). With --workspaces it operates on every
// repository and project folder below the configured workspace roots
// (Discover). Every path a detector reports and every path an action touches
// must be validated through a Guard: the path is made absolute, symlinks are
// resolved and the result must lie inside one of the allowed locations.
// Anything outside is refused with ErrOutsideScope.
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

// errNotImplemented marks skeleton functions that are implemented by the
// milestone issues. It is never returned by a released binary.
var errNotImplemented = errors.New("scope: not implemented yet")

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

// Guard validates that paths lie inside the allowed locations.
type Guard struct {
	allowed []string
}

// NewGuard returns a guard that allows the given locations and everything
// below them. Each location is made absolute and symlink-resolved.
func NewGuard(allowed ...string) (*Guard, error) {
	return nil, errNotImplemented
}

// Resolve makes path absolute, resolves all symlinks in it and verifies the
// result lies inside (or equals) an allowed location. Containment is checked
// component-wise, case-insensitively on case-insensitive filesystems
// (Windows, default macOS). It returns the resolved path or ErrOutsideScope.
func (g *Guard) Resolve(path string) (string, error) {
	return "", errNotImplemented
}

// ResolveParent is like Resolve but resolves only the parent directory and
// keeps the final path element as is. Use it for paths that are themselves
// symlinks which must be removed without following them.
func (g *Guard) ResolveParent(path string) (string, error) {
	return "", errNotImplemented
}

// Allowed returns the resolved allowed locations.
func (g *Guard) Allowed() []string {
	return append([]string(nil), g.allowed...)
}

// FindRepoRoot walks up from start to the nearest directory containing a
// .git entry (directory, or file for linked worktrees and submodules) and
// returns its absolute, symlink-resolved path, or ErrNotInRepo.
func FindRepoRoot(start string) (string, error) {
	return "", errNotImplemented
}

// DiscoverOptions controls workspace discovery.
type DiscoverOptions struct {
	// MaxDepth limits how deep below a root discovery descends (0 = default).
	MaxDepth int
	// Exclude lists glob patterns (matched against the path relative to the
	// root, with forward slashes) of directories to skip.
	Exclude []string
	// DescendIntoRepos makes discovery continue below a found repository to
	// find nested repositories. Off by default because it costs a full walk
	// of every repository.
	DescendIntoRepos bool
}

// Discover walks each root recursively and returns every git repository and
// every non-git project folder inside it as targets (scope = the root). By
// default it does not descend into a repository once found, never descends
// into a project folder's known-huge directories (node_modules, .venv,
// target, ...) and never follows directory symlinks.
func Discover(roots []string, opts DiscoverOptions) ([]Target, error) {
	return nil, errNotImplemented
}
