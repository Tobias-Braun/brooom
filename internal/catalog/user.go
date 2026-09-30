package catalog

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// maxUserDepth bounds how deep below Base a "**" in a user pattern may
// reach, so a stray "**" cannot turn into an unbounded walk of a cache tree.
const maxUserDepth = 8

// PathEnv is everything user pattern expansion needs from the machine. It is
// injectable so tests can simulate all three OSes on one machine.
type PathEnv struct {
	GOOS string
	Home string
	// Getenv looks up an environment variable; nil means nothing is set.
	Getenv func(string) string
}

// HostEnv is the PathEnv of the running machine.
func HostEnv() PathEnv {
	home, _ := os.UserHomeDir()
	return PathEnv{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv}
}

func (e PathEnv) env(name string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(name)
}

// UserLocation is an expanded user-scope entry pattern that exists on this
// machine.
//
// Base contract: Base is the directory that is added to the scope guard, the
// literal directory prefix of the pattern up to the first wildcard segment
// (for wildcard-free patterns the path itself). Because the allowed location
// itself can never be removed (scope.IsAllowedRoot), detectors emit findings
// only strictly below Base, never Base itself. For a wildcard-free pattern
// they therefore report the entries directly inside it; for a wildcard
// pattern they report what the pattern matches below Base. Match implements
// exactly this rule.
type UserLocation struct {
	ToolID   string
	Category Category
	Entry    Entry
	// Pattern is the absolute expanded pattern with native separators.
	Pattern string
	// Base is the absolute native directory to add to the scope guard.
	Base string
	// Rel is the part of the pattern below Base (forward slashes, wildcard
	// segments included); empty for wildcard-free patterns.
	Rel  string
	fold bool
}

// Match reports whether abs (an absolute native path) is something this
// location designates: strictly below Base, of the right kind, and matching
// the pattern (directly inside Base for wildcard-free patterns). "**" reaches
// at most maxUserDepth segments below Base.
func (l UserLocation) Match(abs string, isDir bool) bool {
	if !kindAdmits(l.Entry.Kind, isDir) {
		return false
	}
	rel, ok := relBelow(l.Base, abs, l.fold)
	if !ok {
		return false
	}
	if l.Rel == "" {
		return !strings.Contains(rel, "/")
	}
	if strings.Count(rel, "/")+1 > maxUserDepth {
		return false
	}
	return GlobMatch(l.Rel, rel, l.fold)
}

// UserLocations expands the user-scope entries of the tools in cats (all when
// empty) for env.GOOS and returns those whose Base exists and is readable.
// Patterns whose variable is undefined on that OS are skipped silently, as are
// entries whose os filter excludes it.
func (c *Catalog) UserLocations(env PathEnv, cats ...Category) []UserLocation {
	var out []UserLocation
	for _, t := range c.selected(cats) {
		for _, e := range t.Entries {
			if e.Scope != ScopeUser || !appliesTo(e.OS, env.GOOS) {
				continue
			}
			for _, pat := range e.Patterns {
				x, ok := expandUser(env, pat)
				if !ok || !readable(x.base) {
					continue
				}
				out = append(out, UserLocation{
					ToolID: t.ID, Category: t.Category, Entry: e,
					Pattern: x.pattern, Base: x.base, Rel: x.rel, fold: foldsCase(env.GOOS),
				})
			}
		}
	}
	return out
}

// readable reports whether dir exists and, for directories, can be listed.
func readable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	if fi.IsDir() {
		f, err := os.Open(p)
		if err != nil {
			return false
		}
		_ = f.Close()
	}
	return true
}

// expanded is the result of expanding one user pattern.
type expanded struct {
	pattern, base, rel string
}

// expandUser expands the leading variable and splits the result into Base and
// Rel. It reports false when the variable is undefined on env.GOOS.
func expandUser(env PathEnv, pat string) (expanded, bool) {
	prefix, rest, ok := splitUserPrefix(pat)
	if !ok || rest == "" {
		return expanded{}, false
	}
	if prefix == "~" && strings.HasPrefix(rest, "Library/") && env.GOOS != "darwin" {
		return expanded{}, false
	}
	root, ok := userRoot(env, prefix)
	if !ok {
		return expanded{}, false
	}
	segs := strings.Split(rest, "/")
	lit := 0
	for lit < len(segs) && !hasWildcard(segs[lit]) {
		lit++
	}
	base := path.Join(root, strings.Join(segs[:lit], "/"))
	return expanded{
		pattern: filepath.FromSlash(path.Join(root, rest)),
		base:    filepath.FromSlash(base),
		rel:     strings.Join(segs[lit:], "/"),
	}, true
}

// userRoot resolves a leading variable to a forward-slash directory. XDG
// variables exist on Linux and macOS (unset or relative values fall back to
// the XDG defaults, as the spec requires), %LOCALAPPDATA%/%APPDATA% only on
// Windows.
func userRoot(env PathEnv, prefix string) (string, bool) {
	switch prefix {
	case "~":
		return slashDir(env.Home)
	case "$XDG_CACHE_HOME":
		return xdgDir(env, prefix[1:], ".cache")
	case "$XDG_DATA_HOME":
		return xdgDir(env, prefix[1:], ".local/share")
	case "$XDG_CONFIG_HOME":
		return xdgDir(env, prefix[1:], ".config")
	default:
		if env.GOOS != "windows" {
			return "", false
		}
		return slashDir(env.env(strings.Trim(prefix, "%")))
	}
}

func xdgDir(env PathEnv, name, fallback string) (string, bool) {
	if env.GOOS != "linux" && env.GOOS != "darwin" {
		return "", false
	}
	if v := env.env(name); v != "" && filepath.IsAbs(v) {
		return slashDir(v)
	}
	home, ok := slashDir(env.Home)
	if !ok {
		return "", false
	}
	return path.Join(home, fallback), true
}

// slashDir normalises a directory to forward slashes; an empty value is
// undefined.
func slashDir(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	return path.Clean(filepath.ToSlash(dir)), true
}

// relBelow returns abs relative to base (forward slashes) when abs lies
// strictly below base. Both sides are compared as plain strings so that glob
// metacharacters in the home directory name cannot matter.
func relBelow(base, abs string, fold bool) (string, bool) {
	b, a := path.Clean(filepath.ToSlash(base)), path.Clean(filepath.ToSlash(abs))
	if fold {
		if !strings.HasPrefix(foldCase(a), foldCase(b)+"/") {
			return "", false
		}
	} else if !strings.HasPrefix(a, b+"/") {
		return "", false
	}
	return a[len(b)+1:], true
}

// UserProtection decides whether an absolute path is protected by the user
// scope protect rules of the catalog.
type UserProtection struct {
	rules []userProtect
	fold  bool
}

type userProtect struct {
	base, rel string
	pattern   string
}

// UserProtection expands the user-scope protect patterns of all tools for
// env.GOOS so detectors can reject candidates. Protect rules apply whether or
// not their location exists and whatever categories a detector scans.
func (c *Catalog) UserProtection(env PathEnv) *UserProtection {
	up := &UserProtection{fold: foldsCase(env.GOOS)}
	for _, t := range c.tools {
		for _, p := range t.Protect {
			if p.Scope != ScopeUser || !appliesTo(p.OS, env.GOOS) {
				continue
			}
			for _, pat := range p.Patterns {
				if x, ok := expandUser(env, pat); ok {
					up.rules = append(up.rules, userProtect{base: x.base, rel: x.rel, pattern: x.pattern})
				}
			}
		}
	}
	return up
}

// Patterns returns the expanded absolute protect patterns (native separators).
func (u *UserProtection) Patterns() []string {
	out := make([]string, len(u.rules))
	for i, r := range u.rules {
		out[i] = r.pattern
	}
	return out
}

// Protected reports whether abs matches a protect pattern or lies below a
// protected path.
func (u *UserProtection) Protected(abs string) bool {
	for _, r := range u.rules {
		if r.covers(abs, u.fold) {
			return true
		}
	}
	return false
}

func (r userProtect) covers(abs string, fold bool) bool {
	a, b := filepath.ToSlash(abs), filepath.ToSlash(r.base)
	if r.rel == "" {
		// Wildcard-free: the path itself or anything below it.
		if fold {
			a, b = foldCase(a), foldCase(b)
		}
		return a == b || strings.HasPrefix(a, b+"/")
	}
	rel, ok := relBelow(r.base, abs, fold)
	if !ok {
		return false
	}
	segs := strings.Split(rel, "/")
	for i := range segs {
		if GlobMatch(r.rel, strings.Join(segs[:i+1], "/"), fold) {
			return true
		}
	}
	return false
}
