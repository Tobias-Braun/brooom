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
	want := []string{"after-agents", "tidy", "everything"}
	if got := Names(); !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	// config validates sweep.preset against its own lists to avoid an import
	// cycle; they must never drift.
	if got := config.PresetNames(); !slices.Equal(got, Names()) {
		t.Errorf("config.PresetNames() = %v, presets.Names() = %v", got, Names())
	}
	if got := config.LegacyPresetNames(); !slices.Equal(got, LegacyNames()) {
		t.Errorf("config.LegacyPresetNames() = %v, presets.LegacyNames() = %v", got, LegacyNames())
	}
	if config.DefaultPreset != Everything {
		t.Errorf("default preset %q, want %q", config.DefaultPreset, Everything)
	}
}

func TestGetUnknown(t *testing.T) {
	_, err := Get("reckless")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{`"reckless"`, "after-agents", "tidy", "everything"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if Describe("reckless") != "" {
		t.Error("Describe of an unknown preset should be empty")
	}
}

// TestResolveLegacyNames: the names of earlier releases keep working and all
// run everything, because "safe" was the value `config init` wrote.
func TestResolveLegacyNames(t *testing.T) {
	for _, name := range []string{"safe", "standard", "aggressive"} {
		p, legacy, err := Resolve(name)
		if err != nil || !legacy || p.Name != Everything {
			t.Errorf("Resolve(%q) = %s, %v, %v; want everything, legacy", name, p.Name, legacy, err)
		}
		if _, err := Get(name); err == nil {
			t.Errorf("Get(%q) must not accept a legacy name", name)
		}
	}
	p, legacy, err := Resolve(Tidy)
	if err != nil || legacy || p.Name != Tidy {
		t.Errorf("Resolve(tidy) = %s, %v, %v", p.Name, legacy, err)
	}
	if _, _, err := Resolve("reckless"); err == nil {
		t.Error("Resolve of an unknown name must fail")
	}
}

func TestGetReturnsIndependentCopies(t *testing.T) {
	p := mustGet(t, Everything)
	p.Detectors[0] = "tampered"
	p.Includes[0] = "tampered"
	p.Floors[config.DetectorBuildArtifacts] = findings.ConfidenceLow
	q := mustGet(t, Everything)
	if q.Detectors[0] == "tampered" || q.Includes[0] == "tampered" || q.Floors[config.DetectorBuildArtifacts] != findings.ConfidenceHigh {
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

func TestDetectorSets(t *testing.T) {
	afterAgents, tidy, everything := mustGet(t, AfterAgents), mustGet(t, Tidy), mustGet(t, Everything)
	requireRuns(t, afterAgents, config.DetectorWorktrees, config.DetectorMergedBranch, config.DetectorAIArtifacts)
	requireNotRuns(t, afterAgents, config.DetectorLogs, config.DetectorBuildArtifacts, config.DetectorGitBloat)
	requireRuns(t, tidy, config.DetectorLogs)
	requireNotRuns(t, tidy, config.DetectorWorktrees, config.DetectorMergedBranch, config.DetectorAIArtifacts)
	// everything is the union of the other two plus build artifacts and git
	// maintenance.
	for _, p := range []Preset{afterAgents, tidy} {
		requireRuns(t, everything, p.Detectors...)
	}
	requireRuns(t, everything, config.DetectorBuildArtifacts, config.DetectorGitBloat)
	// Unmerged work is never part of a preset.
	for _, p := range []Preset{afterAgents, tidy, everything} {
		requireNotRuns(t, p, config.DetectorStaleBranch, config.DetectorLargeUntracked)
		for _, d := range p.Detectors {
			if !slices.Contains(config.DetectorNames(), d) {
				t.Errorf("%s names unknown detector %q", p.Name, d)
			}
		}
	}
}

func TestConfidenceFloors(t *testing.T) {
	for _, name := range Names() {
		if got := mustGet(t, name).MinConfidence; got != findings.ConfidenceMedium {
			t.Errorf("%s: MinConfidence %q, want medium", name, got)
		}
	}
	everything := mustGet(t, Everything)
	if got := everything.Floor(config.DetectorBuildArtifacts); got != findings.ConfidenceHigh {
		t.Errorf("everything: build-artifacts floor %q, want high", got)
	}
	if everything.Keeps(config.DetectorBuildArtifacts, findings.ConfidenceMedium) {
		t.Error("everything must not plan build artifacts of active projects (medium)")
	}
	if !everything.Keeps(config.DetectorLogs, findings.ConfidenceMedium) || everything.Keeps(config.DetectorLogs, findings.ConfidenceLow) {
		t.Error("everything: logs must follow MinConfidence")
	}
}

// TestNoPresetLengthensExpiry pins the semantics: no preset changes the
// defaults, and in particular everything must not raise the default
// 2.weeks.ago prune expiry to 90 days (which prunes less, not more).
func TestNoPresetLengthensExpiry(t *testing.T) {
	def := config.Default().Detectors.GitBloat
	for _, name := range Names() {
		got := Apply(config.Default(), mustGet(t, name)).Detectors.GitBloat
		if got.ReflogExpire != def.ReflogExpire || got.PruneExpire != def.PruneExpire {
			t.Errorf("%s changed the default expiry: %q / %q", name, got.ReflogExpire, got.PruneExpire)
		}
	}
}

func TestEverythingOnlyShortensConfiguredExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, reflog, prune, wantReflog, wantPrune string
	}{
		{"longer reflog is shortened to the preset", "365.days.ago", "2.weeks.ago", "90.days.ago", "2.weeks.ago"},
		{"weeks are compared in days", "20.weeks.ago", "13.weeks.ago", "90.days.ago", "90.days.ago"},
		{"prune now stays now", "90.days.ago", "now", "90.days.ago", "now"},
		{"shorter values stay", "30.days.ago", "1.day.ago", "30.days.ago", "1.day.ago"},
		{"never is longer than any date", "never", "never", "90.days.ago", "90.days.ago"},
		{"unparseable values are left alone", "2026-01-01", "yesterday", "2026-01-01", "yesterday"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := config.Default()
			in.Detectors.GitBloat.ReflogExpire = tc.reflog
			in.Detectors.GitBloat.PruneExpire = tc.prune
			got := Apply(in, mustGet(t, Everything)).Detectors.GitBloat
			if got.ReflogExpire != tc.wantReflog || got.PruneExpire != tc.wantPrune {
				t.Errorf("expiry %q / %q, want %q / %q", got.ReflogExpire, got.PruneExpire, tc.wantReflog, tc.wantPrune)
			}
		})
	}
}

func TestOtherPresetsLeaveConfiguredExpiryAlone(t *testing.T) {
	for _, name := range []string{AfterAgents, Tidy} {
		in := config.Default()
		in.Detectors.GitBloat.ReflogExpire = "365.days.ago"
		in.Detectors.GitBloat.PruneExpire = "now"
		got := Apply(in, mustGet(t, name)).Detectors.GitBloat
		if got.ReflogExpire != "365.days.ago" || got.PruneExpire != "now" {
			t.Errorf("%s changed the configured expiry: %q / %q", name, got.ReflogExpire, got.PruneExpire)
		}
	}
}

func TestExpiryDays(t *testing.T) {
	for in, want := range map[string]int{
		"now": 0, "NOW": 0, "never": maxExpiryDays, "1.day.ago": 1, "90.days.ago": 90,
		"2.weeks.ago": 14, "1.week.ago": 7, "0.days.ago": 0, " 3.days.ago ": 3,
	} {
		if got, ok := expiryDays(in); !ok || got != want {
			t.Errorf("expiryDays(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "2026-01-01", "yesterday", "-1.days.ago", "1.month.ago", "1.5.days.ago", "99999999999999999999.days.ago"} {
		if _, ok := expiryDays(in); ok {
			t.Errorf("expiryDays(%q) parsed, want unknown", in)
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

// TestPresetsKeepWorktreeAgeAtZero: agent runs leave fresh worktrees that
// must be removable at once, so no preset may introduce or raise a worktree
// age threshold (#255).
func TestPresetsKeepWorktreeAgeAtZero(t *testing.T) {
	if config.Default().Detectors.Worktrees.MinAgeDays != 0 {
		t.Fatal("default detectors.worktrees.min_age_days must be 0")
	}
	for _, name := range Names() {
		got := Apply(config.Default(), mustGet(t, name))
		if got.Detectors.Worktrees.MinAgeDays != 0 {
			t.Errorf("%s: worktrees.min_age_days = %d, want 0", name, got.Detectors.Worktrees.MinAgeDays)
		}
	}
}

// TestPresetsLeaveAgesAlone: intent presets select what to clean, they do not
// retune how old something must be.
func TestPresetsLeaveAgesAlone(t *testing.T) {
	def := config.Default()
	for _, name := range Names() {
		got := Apply(config.Default(), mustGet(t, name))
		if got.Thresholds != def.Thresholds || got.Detectors.BuildArtifacts.InactiveDays != def.Detectors.BuildArtifacts.InactiveDays {
			t.Errorf("%s changed an age threshold: %+v", name, got.Thresholds)
		}
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
		cfg.Detectors.Worktrees.IncludeStale = true
		got := Apply(cfg, mustGet(t, name))
		switch {
		case got.Thresholds.RecentDays != 9:
			t.Errorf("%s changed RecentDays", name)
		case !slices.Equal(got.Git.ProtectedBranches, cfg.Git.ProtectedBranches):
			t.Errorf("%s changed the protected branches", name)
		case got.Trash.Strategy != config.StrategyQuarantine || got.Trash.AllowDelete:
			t.Errorf("%s changed the trash settings", name)
		case got.Detectors.AIArtifacts.UserLocations || got.Detectors.Logs.UserLocations:
			t.Errorf("%s left user_locations on", name)
		case got.Detectors.Worktrees.IncludeStale:
			t.Errorf("%s reports unmerged worktrees whose upstream is gone", name)
		}
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
// .brooom.json can still tighten on top of it.
func TestRepoConfigStillTightensPresetValue(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Apply(config.Default(), mustGet(t, Everything))
	write(`{"thresholds": {"min_age_days": 20}}`)
	eff, err := cfg.ForTarget("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Thresholds.MinAgeDays != 20 {
		t.Errorf("repo config did not tighten: %d", eff.Thresholds.MinAgeDays)
	}
	write(`{"thresholds": {"min_age_days": 1}}`)
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
		for d, c := range p.Floors {
			if !strings.Contains(text, d+": "+string(c)) {
				t.Errorf("%s: description lacks the %s floor", name, d)
			}
		}
	}
}

func TestWithDetector(t *testing.T) {
	for det, want := range map[string]string{
		config.DetectorMergedBranch:   AfterAgents,
		config.DetectorAIArtifacts:    AfterAgents,
		config.DetectorLogs:           Tidy,
		config.DetectorBuildArtifacts: Everything,
		config.DetectorGitBloat:       Everything,
		config.DetectorStaleBranch:    "",
		config.DetectorLargeUntracked: "",
		"nope":                        "",
	} {
		if got := WithDetector(det); got != want {
			t.Errorf("WithDetector(%q) = %q, want %q", det, got, want)
		}
	}
}
