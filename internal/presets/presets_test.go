package presets

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

func mustGet(t *testing.T, name string) Preset {
	t.Helper()
	p, err := Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNamesOrderAndConfigCrossCheck(t *testing.T) {
	want := []string{"safe", "standard", "aggressive"}
	if got := Names(); !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	// config validates sweep.preset against its own list to avoid an import
	// cycle; the two must never drift.
	if got := config.PresetNames(); !slices.Equal(got, Names()) {
		t.Errorf("config.PresetNames() = %v, presets.Names() = %v", got, Names())
	}
	if !slices.Contains(Names(), config.DefaultPreset) {
		t.Errorf("default preset %q is not a preset", config.DefaultPreset)
	}
}

func TestGetUnknown(t *testing.T) {
	_, err := Get("reckless")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{`"reckless"`, "safe", "standard", "aggressive"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if Describe("reckless") != "" {
		t.Error("Describe of an unknown preset should be empty")
	}
}

func TestGetReturnsIndependentCopies(t *testing.T) {
	p := mustGet(t, Safe)
	p.Detectors[0] = "tampered"
	p.Includes[0] = "tampered"
	if q := mustGet(t, Safe); q.Detectors[0] == "tampered" || q.Includes[0] == "tampered" {
		t.Error("mutating a returned preset changed the shared definition")
	}
}

func requireRuns(t *testing.T, p Preset, detectors ...string) {
	t.Helper()
	for _, d := range detectors {
		if !p.Runs(d) {
			t.Errorf("%s must run %s", p.Name, d)
		}
	}
}

func requireNotRuns(t *testing.T, p Preset, detectors ...string) {
	t.Helper()
	for _, d := range detectors {
		if p.Runs(d) {
			t.Errorf("%s must not run %s", p.Name, d)
		}
	}
}

func TestDetectorSetInvariants(t *testing.T) {
	safe, standard, aggressive := mustGet(t, Safe), mustGet(t, Standard), mustGet(t, Aggressive)
	for _, tc := range []struct{ sub, sup Preset }{{safe, standard}, {standard, aggressive}} {
		for _, d := range tc.sub.Detectors {
			if !tc.sup.Runs(d) {
				t.Errorf("%s misses %s from %s", tc.sup.Name, d, tc.sub.Name)
			}
		}
		if len(tc.sup.Detectors) <= len(tc.sub.Detectors) {
			t.Errorf("%s is not larger than %s", tc.sup.Name, tc.sub.Name)
		}
	}
	requireRuns(t, aggressive, config.DetectorGitBloat, config.DetectorLargeUntracked)
	requireNotRuns(t, safe, config.DetectorStaleBranch, config.DetectorAIArtifacts)
	requireNotRuns(t, safe, config.DetectorGitBloat, config.DetectorLargeUntracked)
	requireNotRuns(t, standard, config.DetectorGitBloat, config.DetectorLargeUntracked)
	known := config.DetectorNames()
	for _, p := range []Preset{safe, standard, aggressive} {
		for _, d := range p.Detectors {
			if !slices.Contains(known, d) {
				t.Errorf("%s names unknown detector %q", p.Name, d)
			}
		}
	}
}

func TestMinConfidence(t *testing.T) {
	for name, want := range map[string]findings.Confidence{
		Safe: findings.ConfidenceHigh, Standard: findings.ConfidenceMedium, Aggressive: findings.ConfidenceMedium,
	} {
		if got := mustGet(t, name).MinConfidence; got != want {
			t.Errorf("%s: MinConfidence %q, want %q", name, got, want)
		}
	}
}

func TestOnlyAggressiveSetsExpiry(t *testing.T) {
	def := config.Default().Detectors.GitBloat
	for _, name := range Names() {
		got := Apply(config.Default(), mustGet(t, name)).Detectors.GitBloat
		if name == Aggressive {
			if got.ReflogExpire != "90.days.ago" || got.PruneExpire != "90.days.ago" {
				t.Errorf("aggressive expiry: %q / %q", got.ReflogExpire, got.PruneExpire)
			}
			continue
		}
		if got.ReflogExpire != def.ReflogExpire || got.PruneExpire != def.PruneExpire {
			t.Errorf("%s changed the expiry: %q / %q", name, got.ReflogExpire, got.PruneExpire)
		}
	}
}

func TestApplyDoesNotMutateInput(t *testing.T) {
	for _, name := range Names() {
		in := config.Default()
		in.Detectors.Logs.Categories = map[string]bool{"os-junk": true}
		in.Detectors.AIArtifacts.UserLocations = true
		snapshot := in.Clone()
		out := Apply(in, mustGet(t, name))
		if !reflect.DeepEqual(in, snapshot) {
			t.Errorf("%s mutated the input config", name)
		}
		if out == in {
			t.Errorf("%s returned the input pointer", name)
		}
		// Writing to the copy's map must not reach the input either.
		out.Detectors.Logs.Categories["os-junk"] = false
		if !in.Detectors.Logs.Categories["os-junk"] {
			t.Errorf("%s: copy aliases the input's category map", name)
		}
	}
}

func TestAggressiveLowersAgesToTable(t *testing.T) {
	got := Apply(config.Default(), mustGet(t, Aggressive))
	if got.Detectors.StaleBranch.MinAgeDays != AggressiveAges.StaleBranchDays ||
		got.Detectors.Worktrees.MinAgeDays != AggressiveAges.WorktreeDays ||
		got.Thresholds.MinAgeDays != AggressiveAges.MinAgeDays ||
		got.Detectors.BuildArtifacts.InactiveDays != AggressiveAges.InactiveDays {
		t.Errorf("thresholds do not match the table: %+v", got.Detectors)
	}
	if got.Detectors.StaleBranch.MinAgeDays != 30 || got.Detectors.Worktrees.MinAgeDays != 14 ||
		got.Thresholds.MinAgeDays != 7 || got.Detectors.BuildArtifacts.InactiveDays != 30 {
		t.Errorf("documented defaults changed: %+v %+v", got.Thresholds, got.Detectors)
	}
	if !got.Detectors.LargeUntracked.IncludeIgnored {
		t.Error("aggressive must include ignored files")
	}
}

// TestNeverRaisesAThreshold covers min(current, presetValue): values the user
// already set lower must stay, values above are lowered.
func TestNeverRaisesAThreshold(t *testing.T) {
	cfg := config.Default()
	cfg.Thresholds.MinAgeDays = 3
	cfg.Detectors.StaleBranch.MinAgeDays = 10
	cfg.Detectors.Worktrees.MinAgeDays = 1
	cfg.Detectors.BuildArtifacts.InactiveDays = 5
	got := Apply(cfg, mustGet(t, Aggressive))
	if got.Thresholds.MinAgeDays != 3 || got.Detectors.StaleBranch.MinAgeDays != 10 ||
		got.Detectors.Worktrees.MinAgeDays != 1 || got.Detectors.BuildArtifacts.InactiveDays != 5 {
		t.Errorf("a lower user value was raised: %+v %+v", got.Thresholds, got.Detectors)
	}

	cfg = config.Default()
	cfg.Thresholds.MinAgeDays = 60
	cfg.Detectors.StaleBranch.MinAgeDays = 365
	got = Apply(cfg, mustGet(t, Aggressive))
	if got.Thresholds.MinAgeDays != 7 || got.Detectors.StaleBranch.MinAgeDays != 30 {
		t.Errorf("higher user values were not lowered: %+v %+v", got.Thresholds, got.Detectors)
	}
}

// TestNeverLoosensSafetySettings pins the values no preset may touch.
func TestNeverLoosensSafetySettings(t *testing.T) {
	for _, name := range Names() {
		cfg := config.Default()
		cfg.Thresholds.RecentDays = 9
		cfg.Git.ProtectedBranches = []string{"main", "keep/*"}
		cfg.Trash.Strategy = config.StrategyQuarantine
		cfg.Trash.AllowDelete = false
		cfg.Detectors.AIArtifacts.UserLocations = true // a preset may only turn this off
		got := Apply(cfg, mustGet(t, name))
		switch {
		case got.Thresholds.RecentDays != 9:
			t.Errorf("%s changed RecentDays", name)
		case !slices.Equal(got.Git.ProtectedBranches, cfg.Git.ProtectedBranches):
			t.Errorf("%s changed the protected branches", name)
		case got.Trash.Strategy != config.StrategyQuarantine || got.Trash.AllowDelete:
			t.Errorf("%s changed the trash settings", name)
		case got.Detectors.AIArtifacts.UserLocations:
			t.Errorf("%s left user_locations on", name)
		}
	}
	// A preset never switches user-level locations on.
	for _, name := range Names() {
		if Apply(config.Default(), mustGet(t, name)).Detectors.AIArtifacts.UserLocations {
			t.Errorf("%s enabled user_locations", name)
		}
	}
}

func TestSafeOverlay(t *testing.T) {
	got := Apply(config.Default(), mustGet(t, Safe))
	if got.Detectors.Worktrees.IncludeStale {
		t.Error("safe must not report stale worktrees")
	}
	for _, cat := range []string{"cache", "crash", "ai", "build"} {
		if enabled, ok := got.Detectors.Logs.Categories[cat]; !ok || enabled {
			t.Errorf("safe must disable log category %q", cat)
		}
	}
	for _, cat := range []string{"os-junk", "logs"} {
		if enabled, ok := got.Detectors.Logs.Categories[cat]; ok && !enabled {
			t.Errorf("safe must keep log category %q", cat)
		}
	}
	std := Apply(config.Default(), mustGet(t, Standard))
	if len(std.Detectors.Logs.Categories) != 0 {
		t.Errorf("standard restricts log categories: %v", std.Detectors.Logs.Categories)
	}
}

func TestAppliedConfigsValidate(t *testing.T) {
	for _, name := range Names() {
		if err := Apply(config.Default(), mustGet(t, name)).Validate(); err != nil {
			t.Errorf("%s produces an invalid config: %v", name, err)
		}
	}
}

// TestRepoConfigStillTightensPresetValue applies the preset first and
// ForTarget second, exactly like the sweep pipeline, and checks that a
// .brooom.json can raise a preset-lowered threshold but not lower it again.
func TestRepoConfigStillTightensPresetValue(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Apply(config.Default(), mustGet(t, Aggressive))
	if cfg.Thresholds.MinAgeDays != 7 {
		t.Fatalf("preset did not lower the threshold: %d", cfg.Thresholds.MinAgeDays)
	}

	write(`{"thresholds": {"min_age_days": 20}}`)
	eff, err := cfg.ForTarget("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Thresholds.MinAgeDays != 20 || eff.Detectors.StaleBranch.MinAgeDays != 30 {
		t.Errorf("repo config did not tighten: %d, stale %d", eff.Thresholds.MinAgeDays, eff.Detectors.StaleBranch.MinAgeDays)
	}

	// 10 is above the preset value 7 but below the global default 14: the
	// preset's lowered value is the baseline, so this is still a tightening.
	write(`{"thresholds": {"min_age_days": 10}}`)
	eff, err = cfg.ForTarget("", dir)
	if err != nil || eff.Thresholds.MinAgeDays != 10 {
		t.Errorf("repo config 10 over preset 7: %v, %+v", err, eff)
	}

	write(`{"thresholds": {"min_age_days": 5}}`)
	if _, err := cfg.ForTarget("", dir); err == nil {
		t.Error("a repo config below the effective value must be rejected")
	}
}

func TestDescribeMentionsEverything(t *testing.T) {
	for _, name := range Names() {
		p := mustGet(t, name)
		text := Describe(name)
		if !strings.HasPrefix(text, name+":") {
			t.Errorf("%s: description starts with %q", name, strings.SplitN(text, "\n", 2)[0])
		}
		for _, d := range p.Detectors {
			if !strings.Contains(text, d) {
				t.Errorf("%s: description lacks detector %s", name, d)
			}
		}
		for _, line := range p.Includes {
			if !strings.Contains(text, line) {
				t.Errorf("%s: description lacks %q", name, line)
			}
		}
		if !strings.Contains(text, string(p.MinConfidence)) {
			t.Errorf("%s: description lacks the confidence", name)
		}
	}
}

func TestWithDetector(t *testing.T) {
	for det, want := range map[string]string{
		config.DetectorMergedBranch:   Safe,
		config.DetectorStaleBranch:    Standard,
		config.DetectorAIArtifacts:    Standard,
		config.DetectorGitBloat:       Aggressive,
		config.DetectorLargeUntracked: Aggressive,
		"nope":                        "",
	} {
		if got := WithDetector(det); got != want {
			t.Errorf("WithDetector(%q) = %q, want %q", det, got, want)
		}
	}
}
