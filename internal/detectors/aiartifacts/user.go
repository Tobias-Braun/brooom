package aiartifacts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tobias-Braun/brooom/internal/catalog"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// userWalkDepth matches the depth limit the catalog applies to "**" in user
// patterns; walking deeper could never produce a match.
const userWalkDepth = 8

// ExtraTargets implements detect.TargetSource. It declares one user target
// per existing repository-keyed ai location of the scanned repositories (for
// Claude Code: ~/.claude/projects/<encoded repository> and the directories of
// its worktrees). The pipeline allows exactly the returned base paths in the
// guard, never a parent such as ~/.claude/projects, which holds the data of
// every other repository, or ~/.claude, whose settings.json stays refused.
// User-level locations that belong to no repository are not scanned.
func (d *Detector) ExtraTargets(_ context.Context, cfg *config.Config, repos []string) ([]scope.Target, error) {
	if len(repos) == 0 {
		return nil, nil
	}
	cat, err := loadCatalog(cfg)
	if err != nil {
		return nil, err
	}
	var out []scope.Target
	seen := map[string]bool{}
	for _, loc := range cat.RepoLocations(d.pathEnv(), repos, catalog.CategoryAI) {
		key := loc.ToolID + "\x00" + loc.Base
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, scope.Target{
			Kind:  scope.TargetUser,
			Path:  loc.Base,
			Scope: findings.Scope{Type: findings.ScopeUser, Path: loc.Base},
			Tool:  loc.ToolID,
		})
	}
	return out, nil
}

// userCandidates enumerates the entries of the locations that belong to the
// target: same tool and same base directory. Targets of tools outside the ai
// category (declared by other detectors) are ignored, so this detector can
// share the pipeline with them.
func (r *run) userCandidates(ctx context.Context) ([]candidate, error) {
	tool, ok := r.cat.Tool(r.target.Tool)
	if !ok || tool.Category != catalog.CategoryAI {
		return nil, nil
	}
	if _, err := os.Stat(r.target.Path); err != nil {
		return nil, fmt.Errorf("ai-artifacts: user location %s: %w", r.target.Path, err)
	}
	env := r.d.pathEnv()
	r.protected = r.cat.UserProtection(env).Protected
	var out []candidate
	for _, loc := range r.cat.RepoLocationsAt(env, r.target.Path, catalog.CategoryAI) {
		if loc.ToolID != r.target.Tool {
			continue
		}
		found, err := r.enumerate(ctx, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return dedupe(out), nil
}

// enumerate lists what one location designates. It never yields the base
// itself: catalog.UserLocation.Match only admits paths strictly below it.
func (r *run) enumerate(ctx context.Context, loc catalog.UserLocation) ([]candidate, error) {
	if loc.Rel == "" {
		return r.directChildren(loc), nil
	}
	col := &collector{}
	opts := walk.Options{Concurrency: r.cfg.Scan.Concurrency, MaxDepth: userWalkDepth}
	err := walk.Walk(ctx, loc.Base, opts, func(e walk.Entry) walk.Decision {
		return r.userVisit(loc, col, e)
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("ai-artifacts: walk %s: %w", loc.Base, err)
	}
	return col.out, nil
}

// userVisit prunes protected paths (nothing below them can be reported) and
// stops at matched directories, which are findings as a whole.
func (r *run) userVisit(loc catalog.UserLocation, col *collector, e walk.Entry) walk.Decision {
	if r.protected(e.Path) {
		return walk.SkipDir
	}
	if c, ok := r.userMatch(loc, e.Path, e.IsDir(), e.IsSymlink()); ok {
		col.add(c)
		return walk.SkipDir
	}
	return walk.Continue
}

// directChildren handles wildcard-free patterns: the entries directly inside
// the base directory. A base that is not a directory yields nothing, since
// the base itself is never a finding.
func (r *run) directChildren(loc catalog.UserLocation) []candidate {
	des, err := os.ReadDir(loc.Base)
	if err != nil {
		return nil
	}
	var out []candidate
	for _, de := range des {
		abs := filepath.Join(loc.Base, de.Name())
		if r.protected(abs) {
			continue
		}
		if c, ok := r.userMatch(loc, abs, de.IsDir(), de.Type()&os.ModeSymlink != 0); ok {
			out = append(out, c)
		}
	}
	return out
}

// userMatch matches one path against the location. A symlink is tried as
// both file and directory, because a link is removed as a link whatever it
// points to.
func (r *run) userMatch(loc catalog.UserLocation, abs string, isDir, symlink bool) (candidate, bool) {
	matched := loc.Match(abs, isDir)
	if !matched && symlink {
		matched = loc.Match(abs, true) || loc.Match(abs, false)
	}
	if !matched {
		return candidate{}, false
	}
	return candidate{
		path:    abs,
		isDir:   isDir && !symlink,
		symlink: symlink,
		toolID:  loc.ToolID,
		entry:   loc.Entry,
		pattern: loc.Pattern,
	}, true
}

// dedupe drops repeated paths (several patterns of one tool may designate
// the same entry), keeping the first.
func dedupe(in []candidate) []candidate {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, c := range in {
		// The paths come from directory listings, so one entry always has
		// one spelling and no case folding is needed here.
		if seen[c.path] {
			continue
		}
		seen[c.path] = true
		out = append(out, c)
	}
	return out
}
