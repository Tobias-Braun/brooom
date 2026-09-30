// Package logs implements the log-and-runtime-files detector: the logs,
// caches, OS junk and crash dumps that general dev tooling (npm, pip, Jest,
// editors, the OS shell) leaves in projects and, when enabled, in well-known
// user-level locations.
//
// What counts as clutter is data, not code: the embedded tool catalog
// (internal/catalog) names the patterns and the protect rules. The detector
// mirrors ai-artifacts, with different categories and one extra safety focus,
// files that a process still has open. Its safety rules, in the order they
// bite:
//
//   - Protect wins. A candidate that is protected, lies below a protected
//     path or is a directory containing one is never reported.
//   - A matched directory that contains a .git entry at any depth is never
//     reported.
//   - User-level locations are scanned only when explicitly enabled, and
//     findings there are always entries inside a location, never the
//     location itself, so the guard's allowed roots stay unremovable.
//   - A file that a process has open (a log still being written) is reported
//     with file_open_by_process and no suggested action, whatever --force says.
//   - Other blocking flags force the suggested action to none; only
//     tracked_files can be lifted with --force.
//
// The detector never modifies anything.
package logs

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
const Name = config.DetectorLogs

func init() { detect.Register(New()) }

// Detector reports log and runtime files. It holds no per-scan state, so one
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
	return "logs, caches, OS junk and crash dumps left by general dev tooling"
}

// Category implements detect.Detector.
func (*Detector) Category() detect.Category { return detect.CategoryLogs }

func (d *Detector) pathEnv() catalog.PathEnv {
	if d.PathEnv != nil {
		return d.PathEnv()
	}
	return catalog.HostEnv()
}

// handled are the catalog categories of this detector. ai belongs to
// ai-artifacts and build to build-artifacts; passing the list to every
// matcher and location lookup keeps those tools out even though the catalog
// holds them.
var handled = []catalog.Category{catalog.CategoryLogs, catalog.CategoryCache, catalog.CategoryOSJunk, catalog.CategoryCrash}

// loadCatalog builds the catalog for the effective configuration: category
// toggles and the user's extra entries are applied by the catalog itself, so
// a disabled category produces nothing anywhere below.
func loadCatalog(cfg *config.Config) (*catalog.Catalog, error) {
	c := cfg.Detectors.Logs
	cat, err := catalog.Load(catalog.Options{
		Extra:           c.Extra,
		Categories:      c.Categories,
		DefaultCategory: catalog.CategoryLogs,
	})
	if err != nil {
		return nil, fmt.Errorf("log-and-runtime-files: catalog: %w", err)
	}
	return cat, nil
}

// Detect implements detect.Detector. It returns an error only when the whole
// target cannot be scanned (missing path, invalid catalog extras); unreadable
// entries are skipped.
func (d *Detector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	switch target.Kind {
	case scope.TargetRepo, scope.TargetProject:
	default:
		return nil
	}
	cfg, err := effectiveConfig(env, target)
	if err != nil {
		return fmt.Errorf("log-and-runtime-files: config for %s: %w", target.Path, err)
	}
	cat, err := loadCatalog(cfg)
	if err != nil {
		return err
	}
	r := &run{d: d, env: env, cfg: cfg, cat: cat, target: target}
	cands, err := r.candidates(ctx)
	if err != nil {
		return err
	}
	cands = verified(cands)
	if len(cands) == 0 {
		return nil
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

// verified drops the candidates whose catalog entry demands a content check
// (crash dumps: a script named core or a database export named *.dmp share the
// name only) that their header fails. Dropping instead of downgrading keeps
// an unverified file out of every plan, so it can never reach the delete
// strategy.
func verified(cands []candidate) []candidate {
	return slices.DeleteFunc(cands, func(c candidate) bool {
		return c.entry.Verify != "" && !catalog.VerifyFile(c.entry.Verify, c.path)
	})
}

// effectiveConfig applies the repository's .brooom.json, exactly like the
// scan pipeline computes it.
func effectiveConfig(env *detect.Env, target scope.Target) (*config.Config, error) {
	return env.Config.ForTarget(target.Path)
}
