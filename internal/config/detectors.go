package config

// Detector names, used as config keys, --detector values and in findings.
const (
	DetectorStaleBranch    = "stale-branch"
	DetectorMergedBranch   = "merged-branch"
	DetectorWorktrees      = "worktrees"
	DetectorGitBloat       = "git-bloat"
	DetectorLargeUntracked = "large-untracked"
	DetectorAIArtifacts    = "ai-artifacts"
	DetectorLogs           = "log-and-runtime-files"
	DetectorBuildArtifacts = "build-artifacts"
)

// DetectorNames returns every known detector name in a stable order.
func DetectorNames() []string {
	return []string{
		DetectorStaleBranch, DetectorMergedBranch, DetectorWorktrees,
		DetectorGitBloat, DetectorLargeUntracked, DetectorAIArtifacts,
		DetectorLogs, DetectorBuildArtifacts,
	}
}

// DetectorEnabled reports whether the named detector is enabled in c.
// Unknown names are reported as disabled.
func (c *Config) DetectorEnabled(name string) bool {
	d := &c.Detectors
	enabled := map[string]bool{
		DetectorStaleBranch:    d.StaleBranch.Enabled,
		DetectorMergedBranch:   d.MergedBranch.Enabled,
		DetectorWorktrees:      d.Worktrees.Enabled,
		DetectorGitBloat:       d.GitBloat.Enabled,
		DetectorLargeUntracked: d.LargeUntracked.Enabled,
		DetectorAIArtifacts:    d.AIArtifacts.Enabled,
		DetectorLogs:           d.Logs.Enabled,
		DetectorBuildArtifacts: d.BuildArtifacts.Enabled,
	}
	return enabled[name]
}
