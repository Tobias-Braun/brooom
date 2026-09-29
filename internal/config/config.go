// Package config defines Brooom's configuration: the JSON file
// ~/.brooom/config.json, its defaults, per-root overrides and the per-repo
// .brooom.json that may only tighten safety rules.
//
// Brooom works with zero configuration inside a repository: Default returns
// the complete default configuration and every field of a loaded file is
// merged on top of it, so a config file only needs to contain what differs.
//
// Durations are stored as whole days (fields ending in Days), sizes as bytes
// (fields ending in Bytes) so the JSON stays unambiguous across platforms.
package config

// CurrentVersion is the version of the config file format.
const CurrentVersion = 1

// Config is the complete configuration.
type Config struct {
	// Version of the file format; files with a newer version are rejected.
	Version int `json:"version"`
	// Roots are the workspace roots scanned with --workspaces.
	Roots []Root `json:"roots"`
	// Thresholds are the global defaults for age and size filters.
	Thresholds Thresholds `json:"thresholds"`
	// Git holds settings shared by all git detectors and actions.
	Git Git `json:"git"`
	// Detectors holds the per-detector settings and toggles.
	Detectors Detectors `json:"detectors"`
	// Trash selects how removed files are disposed of.
	Trash Trash `json:"trash"`
	// Output holds output defaults.
	Output Output `json:"output"`
	// Scan holds walker/cache settings.
	Scan Scan `json:"scan"`
	// Agent holds settings for the (future) agent layer.
	Agent Agent `json:"agent"`
	// UpdateCheck enables `brooom update-check` to contact GitHub. Opt-in.
	UpdateCheck bool `json:"update_check"`
}

// Root is a configured workspace root.
type Root struct {
	// Path of the root; "~" is expanded. Stored as given, resolved on use.
	Path string `json:"path"`
	// Exclude lists glob patterns (relative to the root, forward slashes)
	// of directories that discovery and detectors skip.
	Exclude []string `json:"exclude,omitempty"`
	// Thresholds override the global thresholds for this root. Nil fields
	// inherit the global value.
	Thresholds *ThresholdOverrides `json:"thresholds,omitempty"`
	// Detectors enables or disables detectors for this root by name, e.g.
	// {"build-artifacts": false}.
	Detectors map[string]bool `json:"detectors,omitempty"`
}

// Thresholds are age and size filters shared by the file detectors.
type Thresholds struct {
	// MinAgeDays: findings younger than this are not reported (0 = no limit).
	MinAgeDays int `json:"min_age_days"`
	// MinSizeBytes: findings smaller than this are not reported.
	MinSizeBytes int64 `json:"min_size_bytes"`
	// RecentDays: anything modified within this window gets the
	// recently_modified risk flag.
	RecentDays int `json:"recent_days"`
}

// ThresholdOverrides is Thresholds with optional fields, used for per-root
// and per-repo overrides.
type ThresholdOverrides struct {
	MinAgeDays   *int   `json:"min_age_days,omitempty"`
	MinSizeBytes *int64 `json:"min_size_bytes,omitempty"`
	RecentDays   *int   `json:"recent_days,omitempty"`
}

// Git holds settings shared by the git detectors and actions.
type Git struct {
	// ProtectedBranches are glob patterns of branches that are never
	// suggested for deletion (protected_branch risk flag).
	ProtectedBranches []string `json:"protected_branches"`
	// BaseBranches are candidate base branches for merge detection, in
	// priority order. The remote default branch (origin/HEAD) is always
	// tried first.
	BaseBranches []string `json:"base_branches"`
	// UseGH enables querying open pull requests through the gh CLI when it
	// is installed and authenticated.
	UseGH bool `json:"use_gh"`
}

// Detectors holds one settings block per detector.
type Detectors struct {
	StaleBranch    StaleBranch    `json:"stale-branch"`
	MergedBranch   MergedBranch   `json:"merged-branch"`
	Worktrees      Worktrees      `json:"worktrees"`
	GitBloat       GitBloat       `json:"git-bloat"`
	LargeUntracked LargeUntracked `json:"large-untracked"`
	AIArtifacts    AIArtifacts    `json:"ai-artifacts"`
	Logs           Logs           `json:"log-and-runtime-files"`
	BuildArtifacts BuildArtifacts `json:"build-artifacts"`
}

// StaleBranch configures the stale-branch detector.
type StaleBranch struct {
	Enabled bool `json:"enabled"`
	// MinAgeDays: branches whose last commit is younger are not stale.
	MinAgeDays int `json:"min_age_days"`
	// IncludeUnpushed also reports never-pushed branches (flagged with
	// unpushed_commits, so never suggested without --force).
	IncludeUnpushed bool `json:"include_unpushed"`
}

// MergeMode selects how merged branches are detected.
type MergeMode string

const (
	// MergeAncestor: the branch tip is an ancestor of a base branch.
	MergeAncestor MergeMode = "ancestor"
	// MergeAncestorSquash: additionally detect squash/rebase merges via
	// patch-id comparison (git cherry).
	MergeAncestorSquash MergeMode = "ancestor+squash"
)

// MergedBranch configures the merged-branch detector.
type MergedBranch struct {
	Enabled bool      `json:"enabled"`
	Mode    MergeMode `json:"mode"`
	// IncludeRemote also reports merged remote-tracking branches (suggests
	// nothing by default; remote deletion is out of scope for v1).
	IncludeRemote bool `json:"include_remote"`
}

// Worktrees configures the worktrees detector.
type Worktrees struct {
	Enabled bool `json:"enabled"`
	// IncludeStale also reports worktrees whose branch is stale (not only
	// merged or deleted).
	IncludeStale bool `json:"include_stale"`
	// MinAgeDays is the stale threshold for worktree branches.
	MinAgeDays int `json:"min_age_days"`
}

// GitBloat configures the git-bloat detector and the git purge actions.
type GitBloat struct {
	Enabled bool `json:"enabled"`
	// LooseObjectsThreshold: report when a repo has more loose objects.
	LooseObjectsThreshold int `json:"loose_objects_threshold"`
	// PackCountThreshold: report when a repo has more packs.
	PackCountThreshold int `json:"pack_count_threshold"`
	// ReflogThresholdBytes: report reflogs larger than this.
	ReflogThresholdBytes int64 `json:"reflog_threshold_bytes"`
	// LargeBlobBytes: report blobs in history larger than this (0 = off).
	LargeBlobBytes int64 `json:"large_blob_bytes"`
	// ReflogExpire is passed to git reflog expire --expire (git date
	// syntax, e.g. "90.days.ago").
	ReflogExpire string `json:"reflog_expire"`
	// PruneExpire is passed to git prune --expire / gc --prune.
	PruneExpire string `json:"prune_expire"`
}

// LargeUntracked configures the large-untracked detector.
type LargeUntracked struct {
	Enabled bool `json:"enabled"`
	// MinSizeBytes: untracked/ignored files at least this large are reported.
	MinSizeBytes int64 `json:"min_size_bytes"`
	// IncludeIgnored also reports ignored files (not only untracked ones).
	IncludeIgnored bool `json:"include_ignored"`
}

// AIArtifacts configures the ai-artifacts detector.
type AIArtifacts struct {
	Enabled bool `json:"enabled"`
	// UserLocations enables scanning the user-level well-known locations
	// from the catalog (e.g. ~/.cache/<tool>); off by default.
	UserLocations bool `json:"user_locations"`
	// Tools enables or disables catalog tools by id, e.g. {"cursor": false}.
	// Tools not listed are enabled.
	Tools map[string]bool `json:"tools,omitempty"`
	// Extra adds custom tool entries in the same format as the embedded
	// catalog.
	Extra []CatalogTool `json:"extra,omitempty"`
	// MinAgeDays overrides the global threshold for this detector (nil =
	// global).
	MinAgeDays *int `json:"min_age_days,omitempty"`
}

// Logs configures the log-and-runtime-files detector.
type Logs struct {
	Enabled bool `json:"enabled"`
	// Categories enables or disables entry categories by id (e.g.
	// {"os-junk": false}). Categories not listed are enabled.
	Categories map[string]bool `json:"categories,omitempty"`
	// Extra adds custom entries in catalog format.
	Extra []CatalogTool `json:"extra,omitempty"`
	// MinAgeDays overrides the global threshold for this detector.
	MinAgeDays *int `json:"min_age_days,omitempty"`
}

// BuildArtifacts configures the build-artifacts detector.
type BuildArtifacts struct {
	Enabled bool `json:"enabled"`
	// Dirs are directory names treated as build artifacts when found next
	// to a matching project marker (see the catalog); the default list is
	// embedded. Setting this replaces the default list.
	Dirs []string `json:"dirs,omitempty"`
	// ExtraDirs are added to Dirs.
	ExtraDirs []string `json:"extra_dirs,omitempty"`
	// InactiveDays: a project is inactive when its last commit and newest
	// source file are older than this; only inactive projects get
	// high-confidence findings.
	InactiveDays int `json:"inactive_days"`
}

// CatalogTool is a custom tool/location entry (same schema as the embedded
// catalogs, documented in docs/catalog.md).
type CatalogTool struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Project     []string `json:"project,omitempty"`
	User        []string `json:"user,omitempty"`
	Description string   `json:"description,omitempty"`
}

// TrashStrategy selects how removed files are disposed of.
type TrashStrategy string

const (
	// StrategyTrash moves files to the OS trash (default).
	StrategyTrash TrashStrategy = "trash"
	// StrategyQuarantine moves files to ~/.brooom/quarantine/<session>.
	StrategyQuarantine TrashStrategy = "quarantine"
	// StrategyDelete deletes permanently. Requires explicit opt-in.
	StrategyDelete TrashStrategy = "delete"
)

// Trash configures file disposal.
type Trash struct {
	Strategy TrashStrategy `json:"strategy"`
	// PerDetector overrides the strategy per detector name.
	PerDetector map[string]TrashStrategy `json:"per_detector,omitempty"`
	// QuarantineRetentionDays: quarantined sessions older than this are
	// purged by `brooom purge` (and announced on the next run).
	QuarantineRetentionDays int `json:"quarantine_retention_days"`
	// AllowDelete must be true for StrategyDelete to be usable from config.
	AllowDelete bool `json:"allow_delete"`
}

// Output configures output defaults.
type Output struct {
	// Format is the default --format (table, tree, json, ndjson, plain,
	// summary).
	Format string `json:"format"`
	// Color: "auto" (TTY and NO_COLOR aware), "always" or "never".
	Color string `json:"color"`
}

// Scan configures the walker and scan cache.
type Scan struct {
	// Concurrency is the number of parallel walkers (0 = number of CPUs).
	Concurrency int `json:"concurrency"`
	// Cache enables the mtime-invalidated scan cache in ~/.brooom/cache.
	Cache bool `json:"cache"`
	// SkipDirs are directory names never descended into during discovery
	// and file scans (in addition to built-in skips such as .git).
	SkipDirs []string `json:"skip_dirs,omitempty"`
	// MaxDepth limits workspace discovery depth below each root.
	MaxDepth int `json:"max_depth"`
}

// Agent configures the future agent layer (not used by the v1 CLI).
type Agent struct {
	// Provider: "anthropic" or "openai-compatible".
	Provider string `json:"provider,omitempty"`
	// Endpoint for openai-compatible providers.
	Endpoint string `json:"endpoint,omitempty"`
	// Model name.
	Model string `json:"model,omitempty"`
	// APIKeyEnv names the environment variable holding the API key. Keys are
	// never stored in the config file.
	APIKeyEnv string `json:"api_key_env,omitempty"`
}
