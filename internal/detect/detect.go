// Package detect defines the detector contract and the registry detectors add
// themselves to.
//
// Detectors find, actions act: a detector must never modify the filesystem or
// a repository (no writes, no git commands that take locks or refresh the
// index; use gitx.Runner which sets GIT_OPTIONAL_LOCKS=0). Detectors report
// what they found through the emit callback as findings.Finding values.
//
// Each detector lives in its own package below internal/detectors/<name> and
// registers itself from an init function:
//
//	func init() { detect.Register(New()) }
//
// The CLI imports internal/detectors/all, which blank-imports every detector
// package, so adding a detector never touches shared files other than that
// list.
package detect

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// Category groups detectors for CLI shortcuts (`brooom branches`,
// `brooom artifacts`, ...) and presets.
type Category string

const (
	CategoryGit       Category = "git"
	CategoryFiles     Category = "files"
	CategoryAI        Category = "ai"
	CategoryLogs      Category = "logs"
	CategoryArtifacts Category = "artifacts"
)

// Detector finds clutter and reports it as findings.
type Detector interface {
	// Name is the stable, kebab-case detector name used in findings, config
	// keys, --detector flags and output grouping, e.g. "merged-branch".
	Name() string
	// Description is a one-line human-readable summary for --help and docs.
	Description() string
	// Category groups the detector for CLI shortcuts and presets.
	Category() Category
	// Detect scans the target and calls emit for every finding. It must be
	// safe to call concurrently for different targets, must honour ctx
	// cancellation, and must not modify anything on disk. Returning an error
	// records a ScanError for this target; other targets and detectors
	// continue. A plain error is fatal for the target (the detector could not
	// run, so the scan exits with a distinct code); wrap it with Note when the
	// emitted findings are still trustworthy and the error is only a warning.
	Detect(ctx context.Context, env *Env, target scope.Target, emit func(findings.Finding)) error
}

// Env is the shared, read-only environment passed to detectors. It is
// created once per scan by the engine.
type Env struct {
	// Config is the effective configuration (global config merged with the
	// per-root overrides and the per-repo .brooom.json of the target).
	// Detectors should read their thresholds via Config.ForTarget.
	Config *config.Config
	// Git runs read-only git commands.
	Git gitx.Runner
	// Repos optionally shares one memoizing gitx.Repo per repository between
	// all detectors of a scan. When nil, Repo falls back to uncached handles.
	Repos *gitx.Cache
	// Guard validates that paths lie inside an allowed scope. Detectors must
	// resolve every path they emit through Guard.Resolve.
	Guard *scope.Guard
	// Now is the reference time of the scan; use it instead of time.Now so
	// age computations are consistent and testable.
	Now time.Time
	// Force is true when the user passed --force; detectors still set risk
	// flags, but may suggest actions for blocked findings (e.g. -D for
	// unmerged branches).
	Force bool
	// CacheDir is the directory of the scan caches, or empty when caching
	// is disabled (cfg.Scan.Cache false). The scan pipeline sets it to
	// config.ResolveDirs().Cache. A Fresh walk.DirSize ignores it, so
	// detectors whose NewestModTime feeds an age threshold,
	// recently_modified or LastModified (all of them today) call
	// walk.DirSize with Fresh: true and no CacheDir. A cached
	// NewestModTime is only a lower-bound hint, cached sizes are fine for
	// sizing and ranking.
	CacheDir string
}

// AgeDays returns the whole number of days between t and the scan time.
func (e *Env) AgeDays(t time.Time) int {
	if t.IsZero() || t.After(e.Now) {
		return 0
	}
	return int(e.Now.Sub(t).Hours() / 24)
}

// Repo returns the git repository handle for dir: the shared memoizing one
// when Repos is set, otherwise a fresh uncached handle.
func (e *Env) Repo(ctx context.Context, dir string) (*gitx.Repo, error) {
	if e.Repos != nil {
		return e.Repos.Repo(ctx, dir)
	}
	return gitx.Open(ctx, e.Git, dir)
}

var (
	mu       sync.RWMutex
	registry = map[string]Detector{}
)

// Register adds a detector to the global registry. It panics on duplicate
// names, since that is always a programming error.
func Register(d Detector) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[d.Name()]; dup {
		panic(fmt.Sprintf("detect: duplicate detector %q", d.Name()))
	}
	registry[d.Name()] = d
}

// Get returns the registered detector with the given name.
func Get(name string) (Detector, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := registry[name]
	return d, ok
}

// All returns all registered detectors sorted by name.
func All() []Detector {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Detector, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
