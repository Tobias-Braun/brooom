package scope

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Guard validates that paths lie inside the allowed locations. It is
// immutable after NewGuard and therefore safe for concurrent use.
type Guard struct {
	// allowed mirrors locations[i].path and backs Allowed().
	allowed   []string
	locations []location
	// meta are repository-metadata locations: exact directories (never their
	// content) that only ResolveRepoMeta accepts. See WithRepoMeta.
	meta []location
}

// NewGuard returns a guard that allows the given locations and everything
// below them. Each location is made absolute, symlink-resolved and cleaned;
// duplicates are dropped while nested locations are kept. It refuses an empty
// list, empty or NUL-containing locations, locations that do not exist or are
// not directories, and filesystem, volume or UNC-share roots: allowing a whole
// filesystem would defeat the guard.
//
// The case sensitivity of every location is probed on its own filesystem (see
// probeCaseInsensitive); the guard never writes to disk.
func NewGuard(allowed ...string) (*Guard, error) {
	if len(allowed) == 0 {
		return nil, errors.New("scope: no allowed location given")
	}
	g := &Guard{}
	for _, raw := range allowed {
		loc, err := newLocation(raw)
		if err != nil {
			return nil, err
		}
		if g.hasLocation(loc) {
			continue
		}
		g.locations = append(g.locations, loc)
		g.allowed = append(g.allowed, loc.path)
	}
	return g, nil
}

// newLocation validates one allowed location and probes its filesystem.
func newLocation(raw string) (location, error) {
	if raw == "" {
		return location{}, errors.New("scope: allowed location is empty")
	}
	resolved, err := resolveFull(raw)
	if err != nil {
		return location{}, fmt.Errorf("scope: allowed location %q: %w", raw, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return location{}, fmt.Errorf("scope: allowed location %q: %w", raw, err)
	}
	if !info.IsDir() {
		return location{}, fmt.Errorf("scope: allowed location %q is not a directory", raw)
	}
	if isFilesystemRoot(resolved) {
		return location{}, fmt.Errorf("scope: allowed location %q is a filesystem root, which would allow everything", raw)
	}
	return location{path: resolved, fold: probeCaseInsensitive(resolved)}, nil
}

// hasLocation reports whether loc duplicates an already registered location.
func (g *Guard) hasLocation(loc location) bool {
	for _, have := range g.locations {
		if have.path == loc.path {
			return true
		}
		if (have.fold || loc.fold) && strings.EqualFold(have.path, loc.path) {
			return true
		}
	}
	return false
}

// Resolve makes path absolute, resolves all symlinks in it and verifies the
// result lies inside (or equals) an allowed location. Containment is checked
// component-wise, case-insensitively on locations probed as case-insensitive
// (Windows, default macOS). It returns the resolved path, spelled with the
// allowed location's own prefix, or an error wrapping ErrOutsideScope naming
// both the input and the resolved path.
//
// Paths that do not exist yet are resolved up to their longest existing
// prefix; the remainder is appended and still checked. Symlink loops,
// permission errors and malformed paths return other errors that callers must
// also treat as refusal.
//
// Resolve is a check at call time only; see the package documentation on the
// TOCTOU limit.
func (g *Guard) Resolve(path string) (string, error) {
	resolved, err := resolveFull(path)
	if err != nil {
		return "", fmt.Errorf("scope: resolve %q: %w", path, err)
	}
	out, _, ok := g.match(resolved)
	if !ok {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrOutsideScope, path, resolved)
	}
	return out, nil
}

// ResolveParent is like Resolve but resolves only the parent directory and
// keeps the final path element as is. Use it for paths that are themselves
// symlinks which must be removed without following them: a link that points
// outside but lives inside the scope is allowed, because removing the link
// does not touch its target. A final element of "." or "..", or a path
// without a final element (a root), is refused.
func (g *Guard) ResolveParent(path string) (string, error) {
	if err := validateInput(path); err != nil {
		return "", fmt.Errorf("scope: resolve parent of %q: %w", path, err)
	}
	dir, base, err := splitParent(path)
	if err != nil {
		return "", fmt.Errorf("scope: resolve parent of %q: %w", path, err)
	}
	parent, err := g.Resolve(dir)
	if err != nil {
		return "", fmt.Errorf("scope: resolve parent of %q: %w", path, err)
	}
	// The final element keeps its spelling on purpose (no symlink following),
	// but its 8.3 alias must not hide what it is: GIT~1 names ".git".
	return canonicalLast(joinName(parent, base)), nil
}

// WithRepoMeta returns a copy of g that additionally lets ResolveRepoMeta
// accept exactly the given directories. It exists for the main worktree of a
// linked worktree: git commands for branches and worktrees run there because
// the repository's shared state lives in it, but nothing inside it (files,
// sibling worktrees below it) becomes reachable through Resolve. The same
// validation as NewGuard applies to every path.
func (g *Guard) WithRepoMeta(paths ...string) (*Guard, error) {
	out := &Guard{
		allowed:   append([]string(nil), g.allowed...),
		locations: append([]location(nil), g.locations...),
		meta:      append([]location(nil), g.meta...),
	}
	for _, raw := range paths {
		loc, err := newLocation(raw)
		if err != nil {
			return nil, err
		}
		out.meta = append(out.meta, loc)
	}
	return out, nil
}

// ResolveRepoMeta is Resolve that also accepts a repository-metadata location
// registered with WithRepoMeta, but only that directory itself. Use it only to
// locate the repository a branch or worktree operation runs git in, never
// for paths that are read, walked or removed.
func (g *Guard) ResolveRepoMeta(path string) (string, error) {
	out, err := g.Resolve(path)
	if err == nil || !errors.Is(err, ErrOutsideScope) {
		return out, err
	}
	resolved, rerr := resolveFull(path)
	if rerr != nil {
		return "", err
	}
	for _, loc := range g.meta {
		if respelled, rest, hit := loc.contains(resolved); hit && rest == 0 {
			return respelled, nil
		}
	}
	return "", err
}

// OutsideNote returns OutsideWorktreeHint when path does not resolve inside
// the allowed locations and an empty string when it does. Repository metadata
// locations do not count as inside.
func (g *Guard) OutsideNote(path string) string {
	if _, err := g.Resolve(path); err != nil {
		return OutsideWorktreeHint
	}
	return ""
}

// IsAllowedRoot reports whether path resolves to an allowed location itself.
// Actions use it to refuse removing an allowed root. It returns false on any
// resolution error.
func (g *Guard) IsAllowedRoot(path string) bool {
	resolved, err := resolveFull(path)
	if err != nil {
		return false
	}
	_, isRoot, ok := g.match(resolved)
	return ok && isRoot
}

// Allowed returns the resolved allowed locations.
func (g *Guard) Allowed() []string {
	return append([]string(nil), g.allowed...)
}

// match finds the deepest allowed location containing the resolved path. It
// returns the path respelled with that location's prefix and whether the path
// is the location itself.
func (g *Guard) match(resolved string) (out string, isRoot, ok bool) {
	bestDepth := -1
	for _, loc := range g.locations {
		respelled, rest, hit := loc.contains(resolved)
		if !hit {
			continue
		}
		if d := len(splitComponents(loc.path[len(volumeName(loc.path)):])); d > bestDepth {
			bestDepth = d
			out, isRoot, ok = respelled, rest == 0, true
		}
	}
	return out, isRoot, ok
}
