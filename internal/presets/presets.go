// Package presets defines the no-brainer presets of `brooom sweep`.
//
// A preset is pure data: the detectors to run, the minimum confidence a
// finding needs to be planned and a configuration overlay. Presets can only
// narrow or tune what a scan looks for. They never touch the safety rails:
// blocking risk flags, dry run by default, confirmation, --force semantics,
// the trash strategy, protected branches and the tighten-only .brooom.json are
// all outside their reach (see Apply and the invariants in the tests).
package presets

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// Preset names, least to most aggressive.
const (
	Safe       = "safe"
	Standard   = "standard"
	Aggressive = "aggressive"
)

// Preset is one sweep preset.
type Preset struct {
	// Name is the value of --preset and sweep.preset.
	Name string
	// Summary is a one-line description for help texts.
	Summary string
	// Detectors are the detectors the preset runs. A detector that is not
	// linked into the build is skipped by sweep, not an error.
	Detectors []string
	// MinConfidence is the confidence floor: findings below it are dropped
	// before planning.
	MinConfidence findings.Confidence
	// Overlay adjusts a configuration in place. Callers use Apply, which
	// hands it a private copy.
	Overlay func(*config.Config)
	// Includes is the human-readable bullet list of what the preset does.
	Includes []string
}

// AggressiveAges is the single table of the age thresholds the aggressive
// preset lowers. Every value is applied as min(current, value), so a user who
// configured something lower keeps it. Tune the preset here.
var AggressiveAges = struct {
	// StaleBranchDays is detectors.stale-branch.min_age_days.
	StaleBranchDays int
	// MinAgeDays is thresholds.min_age_days.
	MinAgeDays int
	// InactiveDays is detectors.build-artifacts.inactive_days.
	InactiveDays int
}{
	StaleBranchDays: 30,
	MinAgeDays:      7,
	InactiveDays:    30,
}

// AggressiveExpiry is the git expiry the aggressive preset shortens reflog
// expiry and pruning of unreachable objects to (see shorterExpiry).
const AggressiveExpiry = "90.days.ago"

// safeDetectors is what every preset runs. Standard and aggressive extend it,
// so the superset relation holds by construction.
var safeDetectors = []string{
	config.DetectorMergedBranch,
	config.DetectorWorktrees,
	config.DetectorLogs,
	config.DetectorBuildArtifacts,
}

// nonSafeLogCategories are the log-and-runtime-files categories the safe
// preset switches off: it keeps OS junk and old logs only.
var nonSafeLogCategories = []string{"cache", "crash", "ai", "build"}

// all returns fresh definitions on every call, so callers can never modify the
// shared data by mutating a returned Preset.
func all() []Preset {
	standardDetectors := append(slices.Clone(safeDetectors), config.DetectorStaleBranch, config.DetectorAIArtifacts)
	aggressiveDetectors := append(slices.Clone(standardDetectors), config.DetectorLargeUntracked, config.DetectorGitBloat)
	return []Preset{
		{
			Name:          Safe,
			Summary:       "only high-confidence findings that regenerate or are already merged",
			Detectors:     slices.Clone(safeDetectors),
			MinConfidence: findings.ConfidenceHigh,
			Overlay:       overlaySafe,
			Includes: []string{
				"merged branches",
				"prunable and merged worktrees, clean ones only (no stale worktrees)",
				"OS junk and old logs",
				"build artifacts of inactive projects (active projects rate below high)",
			},
		},
		{
			Name:          Standard,
			Summary:       "safe plus stale branches and AI tool artifacts",
			Detectors:     standardDetectors,
			MinConfidence: findings.ConfidenceMedium,
			Overlay:       overlayStandard,
			Includes: []string{
				"everything in safe, at medium confidence and above",
				"stale branches",
				"AI tool artifacts in projects (never user-level locations)",
				"log and cache categories as configured (safe limits them to OS junk and old logs)",
			},
		},
		{
			Name:          Aggressive,
			Summary:       "standard plus lower age thresholds and git maintenance",
			Detectors:     aggressiveDetectors,
			MinConfidence: findings.ConfidenceMedium,
			Overlay:       overlayAggressive,
			Includes: []string{
				"everything in standard",
				fmt.Sprintf("lower age thresholds: stale branches %d days, minimum age %d days, inactive projects %d days (never raised above your own values)",
					AggressiveAges.StaleBranchDays, AggressiveAges.MinAgeDays, AggressiveAges.InactiveDays),
				"large untracked and ignored files",
				"git gc, reflog expiry and pruning; expiries longer than " + AggressiveExpiry + " are shortened to it, shorter ones are kept",
			},
		},
	}
}

// Names returns the preset names, least to most aggressive.
func Names() []string {
	ps := all()
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return names
}

// Get returns the named preset. The error for an unknown name lists the valid
// ones.
func Get(name string) (Preset, error) {
	for _, p := range all() {
		if p.Name == name {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("unknown preset %q (valid: %s)", name, strings.Join(Names(), ", "))
}

// Describe renders the preset for help texts: a headline, the detectors, the
// confidence floor and the bullet list. It returns "" for an unknown name.
// Help output is generated from it so the text cannot drift from the data.
func Describe(name string) string {
	p, err := Get(name)
	if err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", p.Name, p.Summary)
	fmt.Fprintf(&b, "    detectors: %s\n", strings.Join(p.Detectors, ", "))
	fmt.Fprintf(&b, "    minimum confidence: %s\n", p.MinConfidence)
	for _, line := range p.Includes {
		fmt.Fprintf(&b, "    - %s\n", line)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Apply returns a deep copy of cfg with the preset's overlay applied. The
// loaded configuration is never modified, and callers must apply the result
// before Config.ForTarget so a repository's .brooom.json still tightens on top
// of the preset.
func Apply(cfg *config.Config, p Preset) *config.Config {
	out := cfg.Clone()
	if p.Overlay != nil {
		p.Overlay(out)
	}
	return out
}

// Runs reports whether the preset runs the named detector.
func (p Preset) Runs(detector string) bool { return slices.Contains(p.Detectors, detector) }

// WithDetector returns the name of the least aggressive preset that runs the
// detector, or "" if none does. Used to tell a user which preset to pick.
func WithDetector(detector string) string {
	for _, p := range all() {
		if p.Runs(detector) {
			return p.Name
		}
	}
	return ""
}

// overlaySafe restricts what the detectors report. It only switches things
// off, never on, so a user who disabled more keeps that.
func overlaySafe(c *config.Config) {
	c.Detectors.AIArtifacts.UserLocations = false
	c.Detectors.Worktrees.IncludeStale = false
	if c.Detectors.Logs.Categories == nil {
		c.Detectors.Logs.Categories = map[string]bool{}
	}
	for _, cat := range nonSafeLogCategories {
		c.Detectors.Logs.Categories[cat] = false
	}
}

// overlayStandard keeps AI artifact scanning to the project level. Log
// categories and worktree settings stay as configured: switching a category
// on would override a user's explicit choice to disable it.
func overlayStandard(c *config.Config) {
	c.Detectors.AIArtifacts.UserLocations = false
}

// overlayAggressive lowers the age thresholds (never raising one), reports
// ignored files as well and shortens the git expiries to AggressiveExpiry
// where they are longer (shorter or unknown values stay). It deliberately leaves
// the worktree age threshold at its default 0 (no presets raise it), RecentDays, ProtectedBranches, AllowDelete and the trash strategy alone.
func overlayAggressive(c *config.Config) {
	overlayStandard(c)
	lower(&c.Detectors.StaleBranch.MinAgeDays, AggressiveAges.StaleBranchDays)
	lower(&c.Thresholds.MinAgeDays, AggressiveAges.MinAgeDays)
	lower(&c.Detectors.BuildArtifacts.InactiveDays, AggressiveAges.InactiveDays)
	c.Detectors.LargeUntracked.IncludeIgnored = true
	c.Detectors.GitBloat.ReflogExpire = shorterExpiry(c.Detectors.GitBloat.ReflogExpire, AggressiveExpiry)
	c.Detectors.GitBloat.PruneExpire = shorterExpiry(c.Detectors.GitBloat.PruneExpire, AggressiveExpiry)
}

// maxExpiryDays stands for "never" in expiryDays comparisons.
const maxExpiryDays = math.MaxInt32

// expiryPattern is the only date form the preset comparison understands.
var expiryPattern = regexp.MustCompile(`^(\d{1,9})\.(day|days|week|weeks)\.ago$`)

// expiryDays converts the restricted git expiry forms "now", "never",
// "N.days.ago" and "N.weeks.ago" into days. Brooom has no general git date
// parser (git validates dates), so anything else, such as an absolute date,
// is unknown and reported as not ok.
func expiryDays(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "now":
		return 0, true
	case "never":
		return maxExpiryDays, true
	}
	m := expiryPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	if strings.HasPrefix(m[2], "week") {
		n *= 7
	}
	return n, true
}

// shorterExpiry returns preset when it expires sooner than the configured
// value and the configured value otherwise, like lower does for ages. A value
// that cannot be compared is kept: a preset must not guess about a date the
// user wrote in a form it does not understand, and a longer preset value
// (the aggressive 90 days against the default 2.weeks.ago prune expiry) would
// make the run prune less than the user configured.
func shorterExpiry(configured, preset string) string {
	have, ok := expiryDays(configured)
	if !ok {
		return configured
	}
	want, ok := expiryDays(preset)
	if !ok || want >= have {
		return configured
	}
	return preset
}

// lower sets *v to preset when that is lower, so a threshold the user already
// configured lower stays.
func lower(v *int, preset int) { *v = min(*v, preset) }
