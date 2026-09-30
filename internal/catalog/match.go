package catalog

import (
	"path"
	"path/filepath"
	"strings"
)

// Match is a catalog entry that matched a project-relative path.
type Match struct {
	ToolID   string
	Category Category
	Entry    Entry
}

// ProjectMatcher decides whether a path relative to a project root is
// clutter according to the project-scope entries.
//
// Pattern semantics are gitignore-like: a pattern without "/" matches the
// base name at any depth; a pattern containing "/" is anchored at the project
// root; a leading "**/" means any depth. Paths use forward slashes.
//
// A matcher is immutable and safe for concurrent use.
type ProjectMatcher struct {
	entries []projectEntry
	protect []string
	fold    bool
}

type projectEntry struct {
	toolID   string
	category Category
	entry    Entry
}

// ProjectMatcher returns a matcher for the tools in cats (all tools when cats
// is empty). Entries are filtered by the catalog's OS. Protect rules of every
// tool apply, whatever categories were selected: a protected file is never
// clutter for anyone.
func (c *Catalog) ProjectMatcher(cats ...Category) *ProjectMatcher {
	m := &ProjectMatcher{fold: foldsCase(c.goos)}
	for _, t := range c.selected(cats) {
		for _, e := range t.Entries {
			if e.Scope == ScopeProject && appliesTo(e.OS, c.goos) {
				m.entries = append(m.entries, projectEntry{t.ID, t.Category, e})
			}
		}
	}
	for _, t := range c.tools {
		for _, p := range t.Protect {
			if p.Scope == ScopeProject && appliesTo(p.OS, c.goos) {
				m.protect = append(m.protect, p.Patterns...)
			}
		}
	}
	return m
}

// Match returns the first entry matching rel. It applies the entry kind (a
// "file" entry never matches a directory and vice versa, so a directory named
// "core" is not a crash dump) and protect rules, which win over entries
// whatever their order. Paths that are protected, or lie below a protected
// path, never match.
func (m *ProjectMatcher) Match(rel string, isDir bool) (Match, bool) {
	rel, ok := cleanRel(rel)
	if !ok || m.Protected(rel) {
		return Match{}, false
	}
	for _, pe := range m.entries {
		if !kindAdmits(pe.entry.Kind, isDir) {
			continue
		}
		for _, pat := range pe.entry.Patterns {
			if matchProject(pat, rel, m.fold) {
				return Match{ToolID: pe.toolID, Category: pe.category, Entry: pe.entry}, true
			}
		}
	}
	return Match{}, false
}

// Protected reports whether rel matches a protect pattern or lies below a
// protected path.
func (m *ProjectMatcher) Protected(rel string) bool {
	rel, ok := cleanRel(rel)
	if !ok {
		return false
	}
	segs := strings.Split(rel, "/")
	for i := range segs {
		prefix := strings.Join(segs[:i+1], "/")
		for _, pat := range m.protect {
			if matchProject(pat, prefix, m.fold) {
				return true
			}
		}
	}
	return false
}

// ProtectedAnchored reports whether rel itself matches a protect pattern that
// contains a "/" (a path below the project root such as
// ".claude/settings.local.json"). Unlike Protected it looks at rel alone and
// ignores base-name patterns.
//
// It exists for callers that walk the contents of a directory they are about
// to remove: a directory must not go away with a protected config file inside,
// but base-name patterns such as ".npmrc" or ".gitignore" would match inside
// almost every dependency directory (node_modules) and make every build
// directory unremovable. Anchored patterns only match at their documented
// position below the project root, so they cannot.
func (m *ProjectMatcher) ProtectedAnchored(rel string) bool {
	rel, ok := cleanRel(rel)
	if !ok {
		return false
	}
	for _, pat := range m.protect {
		if strings.Contains(pat, "/") && GlobMatch(pat, rel, m.fold) {
			return true
		}
	}
	return false
}

// cleanRel normalises a project-relative path to forward slashes and rejects
// the project root itself and anything that escapes it.
func cleanRel(rel string) (string, bool) {
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", false
	}
	return rel, true
}

func kindAdmits(k Kind, isDir bool) bool {
	switch k {
	case KindFile:
		return !isDir
	case KindDir:
		return isDir
	default:
		return true
	}
}

// matchProject applies the project pattern semantics to a cleaned rel path.
func matchProject(pattern, rel string, fold bool) bool {
	if !strings.Contains(pattern, "/") {
		return GlobMatch(pattern, path.Base(rel), fold)
	}
	return GlobMatch(pattern, rel, fold)
}
