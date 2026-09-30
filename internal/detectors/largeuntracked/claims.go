package largeuntracked

import (
	"fmt"
	"path"
	"runtime"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/catalog"
	"github.com/Tobias-Braun/brooom/internal/config"
)

// claimSet answers whether another catalog-driven detector owns a path. A
// path is claimed when it equals or lies below something that detector would
// report, so a large file inside node_modules is never reported here: the
// parent directory finding already covers it, and the engine only
// deduplicates by finding ID.
type claimSet interface {
	// Covers reports whether rel (repository-relative, forward slashes) is
	// claimed, either itself or through one of its ancestor directories.
	// isDir tells whether rel is a directory.
	Covers(rel string, isDir bool) bool
}

// defaultBuildDirs is the fallback list of build-artifact directory names,
// used until the build-artifacts detector (#32) exposes its matcher. It is
// intentionally the same kind of list (names, no marker rules): claiming a
// little too much only means this detector stays silent about a directory
// that another detector reports, while claiming too little would duplicate
// findings.
var defaultBuildDirs = []string{
	"node_modules", "bower_components", "dist", "build", "out", "target",
	".venv", "venv", "__pycache__", ".next", ".nuxt", ".turbo", ".gradle",
	".parcel-cache", ".svelte-kit", ".angular", ".tox",
}

// claims is the claimSet built from the configuration of the other detectors.
// A detector that is disabled claims nothing, since it will not report the
// path either.
type claims struct {
	fold      bool
	buildDirs map[string]bool
	matchers  []*catalog.ProjectMatcher
}

// foldsCase reports whether paths compare case-insensitively on this OS
// (default Windows and macOS filesystems).
func foldsCase() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }

// newClaims builds the claims for the effective configuration cfg: build
// artifact directory names plus the project-level patterns of the
// ai-artifacts and log-and-runtime-files catalogs.
func newClaims(cfg *config.Config) (*claims, error) {
	c := &claims{fold: foldsCase()}
	d := cfg.Detectors
	if d.BuildArtifacts.Enabled {
		c.buildDirs = map[string]bool{}
		names := d.BuildArtifacts.Dirs
		if len(names) == 0 {
			names = defaultBuildDirs
		}
		for _, n := range append(append([]string{}, names...), d.BuildArtifacts.ExtraDirs...) {
			c.buildDirs[c.key(n)] = true
		}
	}
	if d.AIArtifacts.Enabled {
		cat, err := catalog.Load(catalog.Options{Extra: d.AIArtifacts.Extra, Tools: d.AIArtifacts.Tools, DefaultCategory: catalog.CategoryAI})
		if err != nil {
			return nil, fmt.Errorf("largeuntracked: ai-artifacts catalog: %w", err)
		}
		c.matchers = append(c.matchers, cat.ProjectMatcher(catalog.CategoryAI))
	}
	if d.Logs.Enabled {
		cat, err := catalog.Load(catalog.Options{Extra: d.Logs.Extra, Categories: d.Logs.Categories, DefaultCategory: catalog.CategoryLogs})
		if err != nil {
			return nil, fmt.Errorf("largeuntracked: log-and-runtime-files catalog: %w", err)
		}
		c.matchers = append(c.matchers, cat.ProjectMatcher(catalog.CategoryLogs, catalog.CategoryCache, catalog.CategoryOSJunk, catalog.CategoryCrash))
	}
	return c, nil
}

func (c *claims) key(name string) string {
	if c.fold {
		return strings.ToLower(name)
	}
	return name
}

// Covers implements claimSet. It is component-wise: every prefix of rel is
// checked, and only the last component may be a file.
func (c *claims) Covers(rel string, isDir bool) bool {
	segs := strings.Split(path.Clean(rel), "/")
	for i := range segs {
		prefix := strings.Join(segs[:i+1], "/")
		prefixIsDir := i < len(segs)-1 || isDir
		if c.claimed(prefix, segs[i], prefixIsDir) {
			return true
		}
	}
	return false
}

func (c *claims) claimed(prefix, name string, isDir bool) bool {
	if isDir && c.buildDirs[c.key(name)] {
		return true
	}
	for _, m := range c.matchers {
		if _, ok := m.Match(prefix, isDir); ok {
			return true
		}
	}
	return false
}
