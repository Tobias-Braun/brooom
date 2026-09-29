package config

import "errors"

// errNotImplemented marks skeleton functions that are implemented by the
// milestone issues. It is never returned by a released binary.
var errNotImplemented = errors.New("config: not implemented yet")

// Load reads the config file at path and merges it over Default. A missing
// file is not an error (defaults are returned). Unknown keys and invalid
// values are errors that name the offending key.
func Load(path string) (*Config, error) {
	return nil, errNotImplemented
}

// Save writes cfg to path atomically (temp file + rename), creating parent
// directories as needed.
func Save(path string, cfg *Config) error {
	return errNotImplemented
}

// Validate checks cfg for invalid values (unknown formats, strategies, merge
// modes, negative thresholds, relative root paths after expansion, ...) and
// returns an error listing every problem.
func (c *Config) Validate() error {
	return errNotImplemented
}

// ForTarget returns the effective configuration for a target directory: the
// global config, overlaid with the matching root's overrides and then with
// the repo's .brooom.json (tighten-only; see ApplyRepoConfig).
func (c *Config) ForTarget(root, target string) (*Config, error) {
	return nil, errNotImplemented
}

// RepoConfig is the per-repo .brooom.json. It can only make Brooom more
// careful: disable detectors, raise age/size thresholds, add protected
// branches and excludes. Anything that would loosen a rule is rejected.
type RepoConfig struct {
	Version int `json:"version"`
	// Disable lists detector names to disable for this repo.
	Disable []string `json:"disable,omitempty"`
	// Thresholds may only raise MinAgeDays/MinSizeBytes/RecentDays.
	Thresholds *ThresholdOverrides `json:"thresholds,omitempty"`
	// ProtectedBranches are added to the global list.
	ProtectedBranches []string `json:"protected_branches,omitempty"`
	// Exclude lists glob patterns (relative to the repo root) to skip.
	Exclude []string `json:"exclude,omitempty"`
}

// ApplyRepoConfig overlays a per-repo config onto c, returning an error if
// rc tries to loosen any rule.
func (c *Config) ApplyRepoConfig(rc *RepoConfig) (*Config, error) {
	return nil, errNotImplemented
}
