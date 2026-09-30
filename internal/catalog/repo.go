package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// RepoPlaceholder is a whole path segment of a user pattern that stands for
// the directory a tool keeps per repository, such as Claude Code's
// ~/.claude/projects/<encoded repository path>. Such an entry never designates
// the directories of all repositories: RepoLocations expands it to the
// directories that belong to the repositories being scanned, and nothing else.
const RepoPlaceholder = "{repo}"

// repoEncoders turn a repository path into the directory name a tool uses for
// it, keyed by Tool.RepoKey.
var repoEncoders = map[string]func(string) string{
	"claude-code": encodeClaudeProject,
}

// repoWorktreeInfixes are the encoded forms of the worktree folders agents
// create inside a repository (.claude/worktrees/<name> and
// .worktrees/<name>). A directory named <encoded repo><infix><name> belongs
// to a worktree of that repository, also after the worktree itself was
// removed, which is exactly when its transcripts are left behind.
var repoWorktreeInfixes = []string{"--claude-worktrees-", "--worktrees-"}

// encodeClaudeProject is how Claude Code names the directory of a project
// below ~/.claude/projects: every character that is not an ASCII letter or
// digit becomes "-", so /Users/me/code/app is -Users-me-code-app.
func encodeClaudeProject(p string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, p)
}

// repoKeys returns what belongs to the repositories: the exact encoded names
// and the prefixes of their worktree folders. The encoding is lossy, so a
// plain prefix of the encoded repository would also claim a sibling such as
// app-site next to app; only the worktree infixes are safe prefixes.
func repoKeys(encode func(string) string, repos []string) (exact map[string]bool, prefixes []string) {
	exact = map[string]bool{}
	for _, r := range repos {
		e := encode(filepath.Clean(r))
		exact[e] = true
		for _, infix := range repoWorktreeInfixes {
			prefixes = append(prefixes, e+infix)
		}
	}
	return exact, prefixes
}

// belongs reports whether the directory name belongs to one of the keys.
func belongs(name string, exact map[string]bool, prefixes []string, fold bool) bool {
	if fold {
		for e := range exact {
			if strings.EqualFold(e, name) {
				return true
			}
		}
		return slices.ContainsFunc(prefixes, func(p string) bool {
			return len(name) > len(p) && strings.EqualFold(name[:len(p)], p)
		})
	}
	return exact[name] || slices.ContainsFunc(prefixes, func(p string) bool {
		return len(name) > len(p) && strings.HasPrefix(name, p)
	})
}

// RepoLocations expands the repository-keyed user entries of the tools in
// cats (all when empty) for the given repository directories. Each directory
// below the placeholder's parent whose name belongs to one of the
// repositories (see repoKeys) yields one location, with that directory as
// Base, so the scope guard allows exactly those directories and never the
// parent that holds the data of every other repository.
func (c *Catalog) RepoLocations(env PathEnv, repos []string, cats ...Category) []UserLocation {
	if len(repos) == 0 {
		return nil
	}
	var out []UserLocation
	for _, t := range c.selected(cats) {
		encode, ok := repoEncoders[t.RepoKey]
		if !ok {
			continue
		}
		exact, prefixes := repoKeys(encode, repos)
		for _, e := range t.Entries {
			if e.Scope != ScopeUser || !appliesTo(e.OS, env.GOOS) {
				continue
			}
			for _, pat := range e.Patterns {
				out = append(out, c.expandRepoPattern(env, t, e, pat, exact, prefixes)...)
			}
		}
	}
	return out
}

// expandRepoPattern lists the placeholder's parent and returns one location
// per directory that belongs to the repositories.
func (c *Catalog) expandRepoPattern(env PathEnv, t Tool, e Entry, pat string, exact map[string]bool, prefixes []string) []UserLocation {
	before, _, ok := strings.Cut(pat, "/"+RepoPlaceholder)
	if !ok {
		return nil
	}
	parent, ok := expandUser(env, before)
	if !ok {
		return nil
	}
	des, err := os.ReadDir(parent.pattern)
	if err != nil {
		return nil
	}
	fold := foldsCase(env.GOOS)
	var out []UserLocation
	for _, de := range des {
		if !de.IsDir() || !belongs(de.Name(), exact, prefixes, fold) {
			continue
		}
		x, ok := expandUser(env, strings.Replace(pat, RepoPlaceholder, de.Name(), 1))
		if !ok || !readable(x.base) {
			continue
		}
		out = append(out, UserLocation{
			ToolID: t.ID, Category: t.Category, Entry: e,
			Pattern: x.pattern, Base: x.base, Rel: x.rel, fold: fold,
		})
	}
	return out
}

// validateRepoPattern checks the placeholder of a user pattern: the tool must
// name a known encoding, the placeholder must be one whole segment used once,
// and no wildcard may come before it (the parent must be one directory).
func validateRepoPattern(pat, repoKey string) error {
	if !strings.Contains(pat, RepoPlaceholder) {
		return nil
	}
	if _, ok := repoEncoders[repoKey]; !ok {
		return fmt.Errorf("pattern %q uses %s, which needs repo_key set to one of %v", pat, RepoPlaceholder, knownRepoKeys())
	}
	segs := strings.Split(pat, "/")
	at := slices.Index(segs, RepoPlaceholder)
	if at < 1 || strings.Count(pat, RepoPlaceholder) != 1 {
		return fmt.Errorf("pattern %q: %s must be one whole path segment, used once", pat, RepoPlaceholder)
	}
	if slices.ContainsFunc(segs[:at], hasWildcard) {
		return fmt.Errorf("pattern %q: no wildcard may come before %s", pat, RepoPlaceholder)
	}
	return nil
}

// knownRepoKeys lists the supported Tool.RepoKey values.
func knownRepoKeys() []string {
	var keys []string
	for k := range repoEncoders {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// RepoLocationsAt returns the repository-keyed locations whose Base is base,
// the directory a TargetSource declared. The repository key is taken from
// base itself (its first segment below the placeholder's parent), so a
// detector can enumerate a target without knowing which repository produced
// it; the scope guard only ever allowed bases RepoLocations produced.
func (c *Catalog) RepoLocationsAt(env PathEnv, base string, cats ...Category) []UserLocation {
	fold := foldsCase(env.GOOS)
	var out []UserLocation
	for _, t := range c.selected(cats) {
		if _, ok := repoEncoders[t.RepoKey]; !ok {
			continue
		}
		for _, e := range t.Entries {
			if e.Scope != ScopeUser || !appliesTo(e.OS, env.GOOS) {
				continue
			}
			for _, pat := range e.Patterns {
				before, _, ok := strings.Cut(pat, "/"+RepoPlaceholder)
				if !ok {
					continue
				}
				parent, ok := expandUser(env, before)
				if !ok {
					continue
				}
				rel, ok := relBelow(parent.pattern, base, fold)
				if !ok {
					continue
				}
				name, _, _ := strings.Cut(rel, "/")
				x, ok := expandUser(env, strings.Replace(pat, RepoPlaceholder, name, 1))
				if !ok || !samePath(x.base, base, fold) {
					continue
				}
				out = append(out, UserLocation{
					ToolID: t.ID, Category: t.Category, Entry: e,
					Pattern: x.pattern, Base: x.base, Rel: x.rel, fold: fold,
				})
			}
		}
	}
	return out
}

// samePath compares two absolute native paths, case-insensitively where the
// file system folds case.
func samePath(a, b string, fold bool) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if fold {
		return strings.EqualFold(a, b)
	}
	return a == b
}
