// Package aiartifacts implements the ai-artifacts detector: run logs,
// transcripts, caches and scratch files that AI coding tools leave in
// projects and, when enabled, in well-known user-level locations.
//
// What counts as clutter is data, not code: the embedded tool catalog
// (internal/catalog) names the patterns and the protect rules. The detector's
// own job is to be careful. Its safety rules, in the order they bite:
//
//   - Protect wins. A candidate that is protected, lies below a protected
//     path or is a directory containing one (a tool's settings, commands,
//     skills, instructions) is never reported.
//   - A matched directory that contains a .git entry at any depth (a nested
//     repository, a linked worktree, a submodule) is never reported.
//   - User-level locations are scanned only when explicitly enabled, and
//     findings there are always entries inside a location, never the
//     location itself, so the guard's allowed roots stay unremovable.
//   - Blocking risk flags force the suggested action to none; only
//     tracked_files can be lifted with --force, an open file never.
//
// The detector never modifies anything.
package aiartifacts

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/catalog"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// Name is the detector name used in findings and config keys.
const Name = config.DetectorAIArtifacts

func init() { detect.Register(New()) }

// Detector reports AI tool artifacts. It holds no per-scan state, so one
// instance serves concurrent targets.
type Detector struct {
	// PathEnv supplies home directory and environment variables for the
	// expansion of user-level patterns; nil means the running machine. It is
	// a field so tests can point the expansion at a temporary home.
	PathEnv func() catalog.PathEnv
}

// New returns the detector with default dependencies.
func New() *Detector { return &Detector{} }

// Name implements detect.Detector.
func (*Detector) Name() string { return Name }

// Description implements detect.Detector.
func (*Detector) Description() string {
	return "run logs, transcripts, caches and scratch files left by AI coding tools"
}

// Category implements detect.Detector.
func (*Detector) Category() detect.Category { return detect.CategoryAI }

func (d *Detector) pathEnv() catalog.PathEnv {
	if d.PathEnv != nil {
		return d.PathEnv()
	}
	return catalog.HostEnv()
}

// loadCatalog builds the catalog for the effective configuration: per-tool
// toggles and the user's extra entries are applied by the catalog itself, so
// a disabled tool produces nothing anywhere below.
func loadCatalog(cfg *config.Config) (*catalog.Catalog, error) {
	c := cfg.Detectors.AIArtifacts
	cat, err := catalog.Load(catalog.Options{
		Extra:           c.Extra,
		Tools:           c.Tools,
		DefaultCategory: catalog.CategoryAI,
	})
	if err != nil {
		return nil, fmt.Errorf("ai-artifacts: catalog: %w", err)
	}
	return cat, nil
}

// Detect implements detect.Detector. It returns an error only when the whole
// target cannot be scanned (missing path, invalid catalog extras); unreadable
// entries are skipped.
func (d *Detector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	switch target.Kind {
	case scope.TargetRepo, scope.TargetProject, scope.TargetUser:
	default:
		return nil
	}
	cfg, err := effectiveConfig(env, target)
	if err != nil {
		return fmt.Errorf("ai-artifacts: config for %s: %w", target.Path, err)
	}
	cat, err := loadCatalog(cfg)
	if err != nil {
		return err
	}
	r := &run{d: d, env: env, cfg: cfg, cat: cat, target: target}
	cands, err := r.candidates(ctx)
	if err != nil || len(cands) == 0 {
		return err
	}
	items, err := r.measureAll(ctx, cands)
	if err != nil {
		return err
	}
	if err := r.flag(ctx, items); err != nil {
		return err
	}
	slices.SortFunc(items, func(a, b *item) int { return strings.Compare(a.path, b.path) })
	for _, it := range items {
		emit(r.finding(it))
	}
	return nil
}

// effectiveConfig applies the per-root and per-repo overlay for project
// targets. User-level targets have no overlay and use the global
// configuration, exactly like the scan pipeline computes it.
func effectiveConfig(env *detect.Env, target scope.Target) (*config.Config, error) {
	if target.Kind == scope.TargetUser {
		return env.Config, nil
	}
	hint := ""
	if target.Scope.Type == findings.ScopeRoot {
		hint = target.Scope.Path
	}
	return env.Config.ForTarget(hint, target.Path)
}
