package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// ForTarget returns the effective configuration for a target directory: the
// global config overlaid with the target's .brooom.json (tighten-only; see
// ApplyRepoConfig). The receiver is never modified. A relative target is an
// error, because the .brooom.json of the working directory would be read.
func (c *Config) ForTarget(target string) (*Config, error) {
	if !filepath.IsAbs(target) {
		return nil, fmt.Errorf("config: target %q is not an absolute path", target)
	}
	eff := c.clone()
	rc, err := LoadRepoConfig(filepath.Join(target, RepoConfigFileName))
	if err != nil {
		return nil, err
	}
	if rc == nil {
		return eff, nil
	}
	return eff.ApplyRepoConfig(rc)
}

// setDetectorEnabled toggles a detector by name; false means unknown name.
func (c *Config) setDetectorEnabled(name string, on bool) bool {
	d := &c.Detectors
	targets := map[string]*bool{
		DetectorStaleBranch:    &d.StaleBranch.Enabled,
		DetectorMergedBranch:   &d.MergedBranch.Enabled,
		DetectorWorktrees:      &d.Worktrees.Enabled,
		DetectorGitBloat:       &d.GitBloat.Enabled,
		DetectorLargeUntracked: &d.LargeUntracked.Enabled,
		DetectorAIArtifacts:    &d.AIArtifacts.Enabled,
		DetectorLogs:           &d.Logs.Enabled,
		DetectorBuildArtifacts: &d.BuildArtifacts.Enabled,
	}
	p, ok := targets[name]
	if ok {
		*p = on
	}
	return ok
}

// ApplyRepoConfig overlays a per-repo config onto c and returns the result as
// a new Config (c is not modified). It is tighten-only: rc may disable known
// detectors, raise (or keep) thresholds, add protected branches and add
// exclude globs. A lower threshold, a negative value, an unknown detector or
// an invalid glob is an error naming the field.
//
// A raised global threshold also floors the per-detector values derived from
// it (new = max(existing, repo value)), so raising min_age_days cannot be
// undone by a more permissive per-detector default.
func (c *Config) ApplyRepoConfig(rc *RepoConfig) (*Config, error) {
	eff := c.clone()
	if rc == nil {
		return eff, nil
	}
	if rc.Version < 0 || rc.Version > CurrentVersion {
		return nil, repoErr("version", "%s", versionMessage(rc.Version, CurrentVersion))
	}
	for _, name := range rc.Disable {
		if !eff.setDetectorEnabled(name, false) {
			return nil, repoErr("disable", "unknown detector %q; known detectors: %s", name, strings.Join(DetectorNames(), ", "))
		}
	}
	if err := eff.tighten(rc.Thresholds); err != nil {
		return nil, err
	}
	if err := eff.addProtected(rc.ProtectedBranches); err != nil {
		return nil, err
	}
	if err := eff.addExcludes(rc.Exclude); err != nil {
		return nil, err
	}
	return eff, nil
}

func repoErr(field, format string, args ...any) error {
	return fmt.Errorf("%s: %s: %s", RepoConfigFileName, field, fmt.Sprintf(format, args...))
}

// tighten raises the global thresholds to the repo values and floors the
// derived per-detector values. c is already a private copy.
func (c *Config) tighten(t *ThresholdOverrides) error {
	if t == nil {
		return nil
	}
	if t.MinAgeDays != nil {
		v, err := raised("thresholds.min_age_days", int64(*t.MinAgeDays), int64(c.Thresholds.MinAgeDays))
		if err != nil {
			return err
		}
		c.raiseMinAge(int(v))
	}
	if t.MinSizeBytes != nil {
		v, err := raised("thresholds.min_size_bytes", *t.MinSizeBytes, c.Thresholds.MinSizeBytes)
		if err != nil {
			return err
		}
		c.Thresholds.MinSizeBytes = v
		c.Detectors.LargeUntracked.MinSizeBytes = max(c.Detectors.LargeUntracked.MinSizeBytes, v)
	}
	if t.RecentDays != nil {
		v, err := raised("thresholds.recent_days", int64(*t.RecentDays), int64(c.Thresholds.RecentDays))
		if err != nil {
			return err
		}
		c.Thresholds.RecentDays = int(v)
	}
	return nil
}

// raised returns want if it is not lower than cur.
func raised(field string, want, cur int64) (int64, error) {
	switch {
	case want < 0:
		return 0, repoErr(field, "%d must not be negative", want)
	case want < cur:
		return 0, repoErr(field, "%d is lower than the effective value %d; repo config may only tighten", want, cur)
	}
	return want, nil
}

// raiseMinAge sets the global min age and floors the per-detector ages.
func (c *Config) raiseMinAge(v int) {
	c.Thresholds.MinAgeDays = v
	d := &c.Detectors
	d.StaleBranch.MinAgeDays = max(d.StaleBranch.MinAgeDays, v)
	d.Worktrees.MinAgeDays = max(d.Worktrees.MinAgeDays, v)
	// A nil per-detector age already means "use the global value".
	for _, p := range []**int{&d.AIArtifacts.MinAgeDays, &d.Logs.MinAgeDays} {
		if *p != nil && **p < v {
			n := v
			*p = &n
		}
	}
}

// addProtected appends valid patterns, skipping duplicates, keeping order.
func (c *Config) addProtected(patterns []string) error {
	for i, p := range patterns {
		if err := validGlob(p); err != nil {
			return repoErr(fmt.Sprintf("protected_branches[%d]", i), "%v", err)
		}
		if !slices.Contains(c.Git.ProtectedBranches, p) {
			c.Git.ProtectedBranches = append(c.Git.ProtectedBranches, p)
		}
	}
	return nil
}

// addExcludes validates the globs and collects them in RepoExclude.
func (c *Config) addExcludes(patterns []string) error {
	for i, p := range patterns {
		if err := validGlob(p); err != nil {
			return repoErr(fmt.Sprintf("exclude[%d]", i), "%v", err)
		}
		if !slices.Contains(c.RepoExclude, p) {
			c.RepoExclude = append(c.RepoExclude, p)
		}
	}
	return nil
}

// cloneMap and cloneSlice copy while preserving nil-ness.
func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return nil
	}
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneCatalog(in []CatalogTool) []CatalogTool {
	if in == nil {
		return nil
	}
	out := make([]CatalogTool, len(in))
	for i, t := range in {
		t.Project, t.User = slices.Clone(t.Project), slices.Clone(t.User)
		t.Entries = cloneCatalogEntries(t.Entries)
		t.Protect = cloneCatalogProtect(t.Protect)
		out[i] = t
	}
	return out
}

func cloneCatalogEntries(in []CatalogEntry) []CatalogEntry {
	if in == nil {
		return nil
	}
	out := make([]CatalogEntry, len(in))
	for i, e := range in {
		e.Patterns, e.OS = slices.Clone(e.Patterns), slices.Clone(e.OS)
		e.MinAgeDays = clonePtr(e.MinAgeDays)
		out[i] = e
	}
	return out
}

func cloneCatalogProtect(in []CatalogProtect) []CatalogProtect {
	if in == nil {
		return nil
	}
	out := make([]CatalogProtect, len(in))
	for i, p := range in {
		p.Patterns, p.OS = slices.Clone(p.Patterns), slices.Clone(p.OS)
		out[i] = p
	}
	return out
}

// Clone returns a deep copy of the configuration including the fields that
// are never serialised. Callers that adjust a loaded configuration for one
// run (sweep presets) work on a clone so the loaded value stays untouched.
func (c *Config) Clone() *Config { return c.clone() }

// clone returns a deep copy including the json:"-" fields, so overlays never
// alias slices, maps or pointers of the receiver.
func (c *Config) clone() *Config {
	n := *c
	n.LegacyRoots = slices.Clone(c.LegacyRoots)
	n.Deprecated = slices.Clone(c.Deprecated)
	n.Git.ProtectedBranches = slices.Clone(c.Git.ProtectedBranches)
	n.Git.BaseBranches = slices.Clone(c.Git.BaseBranches)
	n.Detectors.AIArtifacts.Tools = cloneMap(c.Detectors.AIArtifacts.Tools)
	n.Detectors.AIArtifacts.Extra = cloneCatalog(c.Detectors.AIArtifacts.Extra)
	n.Detectors.AIArtifacts.MinAgeDays = clonePtr(c.Detectors.AIArtifacts.MinAgeDays)
	n.Detectors.Logs.Categories = cloneMap(c.Detectors.Logs.Categories)
	n.Detectors.Logs.Extra = cloneCatalog(c.Detectors.Logs.Extra)
	n.Detectors.Logs.MinAgeDays = clonePtr(c.Detectors.Logs.MinAgeDays)
	n.Detectors.BuildArtifacts.Dirs = slices.Clone(c.Detectors.BuildArtifacts.Dirs)
	n.Detectors.BuildArtifacts.ExtraDirs = slices.Clone(c.Detectors.BuildArtifacts.ExtraDirs)
	n.Trash.PerDetector = cloneMap(c.Trash.PerDetector)
	n.Scan.SkipDirs = slices.Clone(c.Scan.SkipDirs)
	n.RepoExclude = slices.Clone(c.RepoExclude)
	return &n
}
