package config

// Default returns the built-in configuration. Brooom must work with nothing
// but these defaults inside a repository, so every value here is chosen to be
// safe: conservative ages, OS trash, no user-level locations. The one network use is
// git.use_gh (open PR lookup through the gh CLI), which stays on by default
// because it protects branches with open PRs; it is bounded by a timeout and a
// scan-wide circuit breaker and never blocks when gh is missing or offline.
func Default() *Config {
	return &Config{
		Version: CurrentVersion,
		Thresholds: Thresholds{
			MinAgeDays:   DefaultMinAgeDays,
			MinSizeBytes: 0,
			RecentDays:   2,
		},
		Git: Git{
			ProtectedBranches: []string{"main", "master", "develop", "dev", "trunk", "release/*", "release-*", "gh-pages"},
			BaseBranches:      []string{"main", "master", "develop", "trunk"},
			UseGH:             true,
		},
		Detectors: Detectors{
			StaleBranch: StaleBranch{
				Enabled:         true,
				MinAgeDays:      90,
				IncludeUnpushed: true,
			},
			MergedBranch: MergedBranch{
				Enabled: true,
				Mode:    MergeAncestorSquash,
			},
			Worktrees: Worktrees{
				Enabled:      true,
				IncludeStale: true,
				MinAgeDays:   0,
			},
			GitBloat: GitBloat{
				Enabled:               true,
				LooseObjectsThreshold: 5000,
				PackCountThreshold:    50,
				ReflogThresholdBytes:  10 << 20,
				LargeBlobBytes:        50 << 20,
				ReflogExpire:          "90.days.ago",
				PruneExpire:           "2.weeks.ago",
			},
			AIArtifacts: AIArtifacts{
				Enabled: true,
			},
			Logs: Logs{
				Enabled: true,
			},
			BuildArtifacts: BuildArtifacts{
				Enabled:      true,
				InactiveDays: 30,
			},
		},
		Output: Output{
			Format: "table",
			Color:  "auto",
		},
		Scan: Scan{
			Concurrency: 0,
			Cache:       true,
			MaxDepth:    6,
		},
		Sweep: Sweep{Preset: DefaultPreset},
	}
}
