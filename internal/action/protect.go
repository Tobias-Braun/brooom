package action

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/catalog"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// protection applies the catalog protect rules (files such as .env,
// .mcp.json, CLAUDE.local.md or .claude/settings.local.json, and the user
// level configuration of AI tools) to a path that is about to be removed.
//
// The detectors already drop protected candidates, but a findings file is
// untrusted input (`brooom clean --from`), so the trash action, the one place
// that removes files, enforces the same rules again. It never trusts the
// finding's detector or kind: only the path counts.
type protection struct {
	// root is the project root the anchored project patterns hang off: the
	// repository around the path, else the allowed root containing it.
	root     string
	projects []*catalog.ProjectMatcher
	users    []*catalog.UserProtection
}

// newProtection builds the protection for path from the embedded catalog and
// the user's extra catalog entries. Extras can only add protect rules, and
// tool toggles are deliberately ignored: a disabled tool is still protected.
func newProtection(env *Env, path string) (*protection, error) {
	p := &protection{root: projectRoot(env, path)}
	extras := []struct {
		tools []config.CatalogTool
		cat   catalog.Category
	}{{nil, catalog.CategoryAI}}
	if env.Config != nil {
		extras = []struct {
			tools []config.CatalogTool
			cat   catalog.Category
		}{
			{env.Config.Detectors.AIArtifacts.Extra, catalog.CategoryAI},
			{env.Config.Detectors.Logs.Extra, catalog.CategoryLogs},
		}
	}
	for _, x := range extras {
		cat, err := catalog.Load(catalog.Options{Extra: x.tools, DefaultCategory: x.cat})
		if err != nil {
			return nil, err
		}
		p.projects = append(p.projects, cat.ProjectMatcher())
		p.users = append(p.users, cat.UserProtection(catalog.HostEnv()))
	}
	return p, nil
}

// projectRoot is the directory relative project patterns are matched against.
func projectRoot(env *Env, path string) string {
	start := path
	if fi, err := os.Lstat(path); err != nil || !fi.IsDir() {
		start = filepath.Dir(path)
	}
	root, err := scope.FindRepoRoot(start)
	if err == nil {
		return root
	}
	if !errors.Is(err, scope.ErrNotInRepo) {
		return filepath.Dir(path)
	}
	best := ""
	for _, r := range env.Guard.Allowed() {
		if covers(r, path) && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return filepath.Dir(path)
	}
	return best
}

// rels returns abs relative to every directory from the project root down to
// limit, the parent of the removal target. Outside a repository the root is
// only the allowed root, which can sit far above the project, so an anchored
// pattern such as ".claude/settings.local.json" must be tried at each level
// to line up with the project directory. Levels never go below the target's
// parent: inside a directory about to be removed an anchored pattern must not
// match relative to a nested package (node_modules/pkg/.github/prompts).
func (p *protection) rels(abs, limit string) []string {
	var out []string
	dir := p.root
	for {
		r, err := filepath.Rel(dir, abs)
		if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return out
		}
		out = append(out, filepath.ToSlash(r))
		if dir == limit || !covers(dir, limit) {
			return out
		}
		first, _, more := strings.Cut(filepath.ToSlash(r), "/")
		if !more {
			return out
		}
		dir = filepath.Join(dir, filepath.FromSlash(first))
	}
}

// target reports why the path itself must not be removed: it is protected or
// lies below a protected path. The reason is empty when it may go on.
func (p *protection) target(path string) string {
	if p == nil {
		return ""
	}
	for _, rel := range p.rels(path, filepath.Dir(path)) {
		for _, m := range p.projects {
			if m.Protected(rel) {
				return "refusing to remove " + rel + ": protected by the catalog (it is, or lies inside, a protected file or directory)"
			}
		}
	}
	for _, u := range p.users {
		if u.Protected(path) {
			return "refusing to remove " + path + ": protected by the catalog (user-level tool configuration)"
		}
	}
	return ""
}

// entryCheck returns the check for entries below a directory target, or nil
// when there is nothing to check. Project rules use anchored patterns only
// (see catalog.ProjectMatcher.ProtectedAnchored), user rules the full match.
func (p *protection) entryCheck(target string) entryCheck {
	if p == nil {
		return nil
	}
	return func(e walk.Entry) bool {
		for _, rel := range p.rels(e.Path, filepath.Dir(target)) {
			for _, m := range p.projects {
				if m.ProtectedAnchored(rel) {
					return true
				}
			}
		}
		for _, u := range p.users {
			if u.Protected(e.Path) {
				return true
			}
		}
		return false
	}
}
