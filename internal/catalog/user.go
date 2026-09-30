package catalog

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/scope"
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
	base := joinSlash(root, strings.Join(segs[:lit], "/"))
	return expanded{
		pattern: filepath.FromSlash(joinSlash(root, rest)),
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
	return joinSlash(home, fallback), true
}

// slashDir normalises a directory to forward slashes; an empty value is
// undefined.
func slashDir(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	return cleanSlash(filepath.ToSlash(dir)), true
}

// splitVolume separates a Windows volume from the rest of a path, normalising
// backslashes when the path starts with two separators. It is deliberately
// independent of the host OS so that Windows layouts (UNC shares, "\\?\"
// prefixes, drive letters) are handled identically when simulated on another
// machine. The flip side is that on POSIX hosts a path such as "//a/b/c" or
// "c:/x" is split as a volume too; those spellings do not occur in real
// homes and the split only serves consistent cleaning and comparison. The
// volume is one of "//server/share", "//?/UNC/server/share",
// "//?/C:" (and the "//./" device spelling) or "X:"; it is empty for ordinary
// paths.
func splitVolume(p string) (vol, rest string) {
	if len(p) >= 2 && isSep(p[0]) && isSep(p[1]) {
		return splitUNC(strings.ReplaceAll(p, `\`, "/"))
	}
	if len(p) >= 2 && p[1] == ':' && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z') {
		return p[:2], p[2:]
	}
	return "", p
}

func isSep(c byte) bool { return c == '/' || c == '\\' }

// splitUNC splits a forward-slash path that starts with "//" into its
// "//server/share" or "//?/..." volume and the rest.
func splitUNC(p string) (vol, rest string) {
	parts := strings.SplitN(p[2:], "/", 5)
	n := 2 // server and share
	if parts[0] == "?" || parts[0] == "." {
		if len(parts) > 1 && strings.EqualFold(parts[1], "UNC") {
			n = 4
		}
	}
	if len(parts) < n {
		return p, ""
	}
	vol = "//" + strings.Join(parts[:n], "/")
	return vol, p[len(vol):]
}

// cleanSlash is path.Clean that keeps a Windows volume intact: only the part
// after the volume is cleaned, because path.Clean would collapse the leading
// "//" of a UNC path into a single slash and thereby point at the current
// drive instead of the share.
func cleanSlash(p string) string {
	vol, rest := splitVolume(p)
	if vol == "" {
		return path.Clean(rest)
	}
	rest = strings.ReplaceAll(rest, `\`, "/")
	if c := path.Clean("/" + rest); c != "/" {
		return vol + c
	}
	return vol
}

// joinSlash joins forward-slash elements below root, volume-aware.
func joinSlash(root string, elem ...string) string {
	return cleanSlash(strings.Join(append([]string{root}, elem...), "/"))
}

// relBelow returns abs relative to base (forward slashes) when abs lies
// strictly below base. Both sides are compared as plain strings so that glob
// metacharacters in the home directory name cannot matter.
func relBelow(base, abs string, fold bool) (string, bool) {
	b, a := cleanSlash(filepath.ToSlash(base)), cleanSlash(filepath.ToSlash(abs))
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
	// alias marks the symlink-resolved twin of a rule; it only exists for
	// matching and is never listed by Patterns.
	alias bool
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
				x, ok := expandUser(env, pat)
				if !ok {
					continue
				}
				up.rules = append(up.rules, userProtect{base: x.base, rel: x.rel, pattern: x.pattern})
				// A symlinked HOME, a stow/chezmoi-managed ~/.claude or a
				// junction below %APPDATA% makes the same location reachable
				// under a second spelling; the trash action compares the
				// guard-resolved one.
				if rb := resolveExisting(x.base); rb != x.base {
					up.rules = append(up.rules, userProtect{base: rb, rel: x.rel, alias: true})
				}
			}
		}
	}
	return up
}

// Patterns returns the expanded absolute protect patterns (native separators).
func (u *UserProtection) Patterns() []string {
	out := make([]string, 0, len(u.rules))
	for _, r := range u.rules {
		if !r.alias {
			out = append(out, r.pattern)
		}
	}
	return out
}

// Protected reports whether abs matches a protect pattern or lies below a
// protected path. abs is tried as given and symlink-resolved, against every
// rule in its lexical and resolved spelling, so it does not matter which
// spelling the caller holds.
func (u *UserProtection) Protected(abs string) bool {
	if u.coveredBy(abs) {
		return true
	}
	// Symlink resolution touches the disk, so it only happens when the
	// lexical spelling did not already match.
	if r := resolveExisting(abs); r != abs {
		return u.coveredBy(r)
	}
	return false
}

// coveredBy reports whether any rule, in either spelling, covers abs.
func (u *UserProtection) coveredBy(abs string) bool {
	for _, r := range u.rules {
		if r.covers(abs, u.fold) {
			return true
		}
	}
	return false
}

// resolveExisting resolves symlinks and Windows junctions in the existing
// prefix of p and keeps the part that does not exist, so locations that are
// not there yet still get a comparable spelling. It uses the scope guard's
// resolver, because filepath.EvalSymlinks no longer follows junctions (Go
// 1.23, winsymlink=1) and the trash action compares guard-resolved paths.
// When p cannot be resolved it is returned unchanged.
func resolveExisting(p string) string {
	if r, err := scope.Resolve(p); err == nil {
		return r
	}
	return p
}

func (r userProtect) covers(abs string, fold bool) bool {
	a, b := cleanSlash(filepath.ToSlash(abs)), cleanSlash(filepath.ToSlash(r.base))
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
