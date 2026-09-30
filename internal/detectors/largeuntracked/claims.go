package largeuntracked

import (
	"fmt"
	"path"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/catalog"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detectors/buildartifacts"
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

// claims is the claimSet built from the configuration of the other detectors.
// A detector that is disabled claims nothing, since it will not report the
// path either.
//
// Build artifacts are claimed through the build-artifacts detector's own
// matcher (buildartifacts.ClaimsWith), markers included, so the two detectors
// can never double-report a directory and a common name like dist without a
// project marker stays reportable here.
type claims struct {
	buildDirs func(rel string, isDir bool) bool
	matchers  []*catalog.ProjectMatcher
}

// newClaims builds the claims for the effective configuration cfg of the
// repository at dir: the build-artifacts matcher plus the project-level patterns of the
// ai-artifacts and log-and-runtime-files catalogs.
func newClaims(dir string, cfg *config.Config) (*claims, error) {
	c := &claims{}
	d := cfg.Detectors
	if d.BuildArtifacts.Enabled {
		fn, err := buildartifacts.ClaimsWith(dir, d.BuildArtifacts)
		if err != nil {
			return nil, fmt.Errorf("largeuntracked: %w", err)
		}
		c.buildDirs = fn
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

// Covers implements claimSet. It is component-wise: every prefix of rel is
// checked, and only the last component may be a file.
func (c *claims) Covers(rel string, isDir bool) bool {
	segs := strings.Split(path.Clean(rel), "/")
	for i := range segs {
		prefix := strings.Join(segs[:i+1], "/")
		prefixIsDir := i < len(segs)-1 || isDir
		if c.claimed(prefix, prefixIsDir) {
			return true
		}
	}
	return false
}

func (c *claims) claimed(prefix string, isDir bool) bool {
	if isDir && c.buildDirs != nil && c.buildDirs(prefix, true) {
		return true
	}
	for _, m := range c.matchers {
		if _, ok := m.Match(prefix, isDir); ok {
			return true
		}
	}
	return false
}
