// Package presets defines the intent presets of `brooom sweep`.
//
// A preset is pure data: the detectors to run, the minimum confidence a
// finding needs to be planned and a configuration overlay. Presets are named
// after what the user wants to do (clean up after an agent run, tidy up,
// sweep everything), not after how risky they are, because every preset keeps
// the same safety rails: blocking risk flags, the confirmation before anything
// is changed, the trash strategy, protected branches and the tighten-only
// .brooom.json are all outside their reach (see Apply and the invariants in
// the tests). No preset removes unmerged work: stale branches and large
// untracked files are left to `brooom scan -d ...`.
package presets

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// Preset names, narrowest first.
const (
	AfterAgents = "after-agents"
	Tidy        = "tidy"
	Everything  = "everything"
)

// Preset is one sweep preset.
type Preset struct {
	// Name is the positional argument of `brooom sweep` and sweep.preset.
	Name string
	// Summary is a one-line description for help texts.
	Summary string
	// Detectors are the detectors the preset runs. A detector that is not
	// linked into the build is skipped by sweep, not an error.
	Detectors []string
	// MinConfidence is the confidence floor: findings below it are dropped
	// before planning.
	MinConfidence findings.Confidence
	// Floors raise the confidence floor for single detectors above
	// MinConfidence. Build artifacts of a project that is still being worked
	// on only reach medium confidence, and trashing its node_modules in the
	// middle of the work is not what "everything" means.
	Floors map[string]findings.Confidence
	// Overlay adjusts a configuration in place. Callers use Apply, which
	// hands it a private copy.
	Overlay func(*config.Config)
	// Includes is the human-readable bullet list of what the preset does.
	Includes []string
}

// Expiry is the git expiry the everything preset shortens reflog expiry and
// pruning of unreachable objects to (see shorterExpiry).
const Expiry = "90.days.ago"

// legacyNames are the preset names of earlier releases. They still resolve,
// to everything, because `brooom config init` wrote "safe" into every config
// file as the default, so a legacy name in a config is rarely a deliberate
// choice of a narrower preset.
var legacyNames = []string{"safe", "standard", "aggressive"}

func all() []Preset {
	return []Preset{
		{
			Name:          AfterAgents,
			Summary:       "clean up after an agent run: merged worktrees and branches, agent leftovers",
			Detectors:     []string{config.DetectorAIArtifacts, config.DetectorMergedBranch, config.DetectorWorktrees},
			MinConfidence: findings.ConfidenceMedium,
			Overlay:       overlayCommon,
			Includes: []string{
				"worktrees whose branch is merged (squash and rebase merges too), clean ones only",
				"local branches merged into the base branch",
				"AI tool artifacts in the repository: run logs, transcripts, caches, scratch files",
			},
		},
		{
			Name:          Tidy,
			Summary:       "low-risk hygiene: logs, OS junk, test caches and coverage output",
			Detectors:     []string{config.DetectorLogs},
			MinConfidence: findings.ConfidenceMedium,
			Overlay:       overlayCommon,
			Includes: []string{
				"debug and rotated logs, crash dumps, editor swap files",
				".DS_Store, Thumbs.db and other OS junk",
				"test caches and coverage output",
			},
		},
		{
			Name:    Everything,
			Summary: "all of the above plus build artifacts of inactive projects and git maintenance",
			Detectors: []string{
				config.DetectorAIArtifacts, config.DetectorBuildArtifacts, config.DetectorGitBloat,
				config.DetectorLogs, config.DetectorMergedBranch, config.DetectorWorktrees,
			},
			MinConfidence: findings.ConfidenceMedium,
			Floors:        map[string]findings.Confidence{config.DetectorBuildArtifacts: findings.ConfidenceHigh},
			Overlay:       overlayEverything,
			Includes: []string{
				"everything in after-agents and tidy",
				"build artifacts (node_modules, target, .venv, ...) of inactive projects only",
				"git gc, reflog expiry and pruning; expiries longer than " + Expiry + " are shortened to it, shorter ones are kept",
			},
		},
	}
}

// Names returns the preset names, narrowest first.
func Names() []string {
	ps := all()
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return names
}

// LegacyNames returns the preset names of earlier releases that still resolve
// (see Resolve).
func LegacyNames() []string { return slices.Clone(legacyNames) }

// Get returns the named preset. The error for an unknown name lists the valid
// ones. Legacy names are unknown here; Resolve accepts them.
func Get(name string) (Preset, error) {
	for _, p := range all() {
		if p.Name == name {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("unknown preset %q (valid: %s)", name, strings.Join(Names(), ", "))
}

// Resolve is Get plus the legacy names, which resolve to everything. legacy
// reports that the name was a legacy one, so the caller can say which preset
// runs instead.
func Resolve(name string) (p Preset, legacy bool, err error) {
	if slices.Contains(legacyNames, name) {
		p, err = Get(Everything)
		return p, true, err
	}
	p, err = Get(name)
	return p, false, err
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
	fmt.Fprintf(&b, "    minimum confidence: %s", p.MinConfidence)
	for _, d := range slices.Sorted(maps.Keys(p.Floors)) {
		fmt.Fprintf(&b, " (%s: %s)", d, p.Floors[d])
	}
	b.WriteString("\n")
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

// Floor returns the confidence a finding of the detector needs to be planned.
func (p Preset) Floor(detector string) findings.Confidence {
	if c, ok := p.Floors[detector]; ok {
		return c
	}
	return p.MinConfidence
}

// Keeps reports whether the preset plans a finding with this detector and
// confidence.
func (p Preset) Keeps(detector string, c findings.Confidence) bool {
	return c.Rank() >= p.Floor(detector).Rank()
}

// WithDetector returns the name of the narrowest preset that runs the
// detector, or "" if none does. Used to tell a user which preset to pick.
func WithDetector(detector string) string {
	for _, p := range all() {
		if p.Runs(detector) {
			return p.Name
		}
	}
	return ""
}

// overlayCommon keeps every preset to merged work: worktrees whose upstream is
// gone are not merged, so they are left to `brooom review`. User-level tool
// locations stay off; a preset only looks inside the repository. It only
// switches things off, never on, so a user who disabled more keeps that.
func overlayCommon(c *config.Config) {
	c.Detectors.AIArtifacts.UserLocations = false
	c.Detectors.Logs.UserLocations = false
	c.Detectors.Worktrees.IncludeStale = false
}

// overlayEverything is overlayCommon plus the git expiries shortened to
// Expiry where they are longer (shorter or unknown values stay). Age
// thresholds, ProtectedBranches, AllowDelete and the trash strategy are left
// alone.
func overlayEverything(c *config.Config) {
	overlayCommon(c)
	c.Detectors.GitBloat.ReflogExpire = shorterExpiry(c.Detectors.GitBloat.ReflogExpire, Expiry)
	c.Detectors.GitBloat.PruneExpire = shorterExpiry(c.Detectors.GitBloat.PruneExpire, Expiry)
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
// value and the configured value otherwise. A value that cannot be compared
// is kept: a preset must not guess about a date the user wrote in a form it
// does not understand, and a longer preset value (90 days against the default
// 2.weeks.ago prune expiry) would make the run prune less than the user
// configured.
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
