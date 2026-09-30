package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// checkConfig applies the user's configuration to a finding of the file, the
// way a scan would have: a finding of a detector that is disabled for the
// place it lies in (globally, per root or by the tighten-only .brooom.json)
// and a path below an excluded directory are refused. The detectors never
// report them, so such a finding can only come from an edited or foreign
// file. Configuration is derived from the resolved path, never from the
// scope the file claims.
//
// User-level findings only see the global configuration: there is no project
// for a .brooom.json or an exclude list to belong to.
func (sc *cleanScope) checkConfig(f findings.Finding, resolved string) string {
	if f.Scope.Type == findings.ScopeUser {
		return disabledReason(sc.cfg, f.Detector)
	}
	target := sc.projectTarget(resolved)
	if target == "" {
		return ""
	}
	cfg, err := sc.targetConfig(target)
	if err != nil {
		return fmt.Sprintf("configuration for %s cannot be loaded: %v", target, err)
	}
	if reason := disabledReason(cfg, f.Detector); reason != "" {
		return reason
	}
	if excludedBelow(cfg.RepoExclude, target, resolved) || excludedBelow(cfg.RootExclude, cfg.RootPath, resolved) {
		return "path is excluded by the configuration (exclude)"
	}
	return ""
}

// disabledReason words the refusal of a finding of a disabled detector.
func disabledReason(cfg *config.Config, detector string) string {
	if detectorEnabled(cfg, detector) {
		return ""
	}
	return fmt.Sprintf("detector %s is disabled by the configuration", detector)
}

// projectTarget returns the scan target a resolved path belongs to: the
// deepest repository of this run's scope containing it, else the deepest
// allowed project location. It mirrors the targets a scan would have used, so
// ForTarget yields the configuration the detector ran with. Empty when the
// path is in none (checkPath has already refused those).
func (sc *cleanScope) projectTarget(resolved string) string {
	best := ""
	for _, c := range append(append([]string{}, sc.repos...), sc.project.Allowed()...) {
		if within(c, resolved) && len(c) > len(best) {
			best = c
		}
	}
	return best
}

// targetConfig returns the effective configuration of a target, memoized: a
// file usually holds many findings of one repository.
func (sc *cleanScope) targetConfig(target string) (*config.Config, error) {
	if cfg, ok := sc.targetCfgs[target]; ok {
		return cfg, nil
	}
	cfg, err := sc.cfg.ForTarget("", target)
	if err != nil {
		return nil, err
	}
	if sc.targetCfgs == nil {
		sc.targetCfgs = map[string]*config.Config{}
	}
	sc.targetCfgs[target] = cfg
	return cfg, nil
}

// within reports whether path equals dir or lies below it, lexically and
// with the case rules of the platform (gitx.SamePath).
func within(dir, path string) bool {
	if gitx.SamePath(dir, path) {
		return true
	}
	for p := filepath.Dir(path); p != path; path, p = p, filepath.Dir(p) {
		if gitx.SamePath(dir, p) {
			return true
		}
	}
	return false
}

// excludedBelow reports whether abs lies below base and the path, or any
// directory on the way to it, matches an exclude pattern. Detectors prune
// excluded directories, so everything below one is excluded too.
func excludedBelow(patterns []string, base, abs string) bool {
	if len(patterns) == 0 || base == "" {
		return false
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	for i := range segs {
		if scope.Excluded(patterns, strings.Join(segs[:i+1], "/")) {
			return true
		}
	}
	return false
}
