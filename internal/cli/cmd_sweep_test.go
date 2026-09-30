package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/presets"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// snapshot fingerprints everything a dry run must not change: every file
// outside .git (path, mode and content), the refs and the worktree list.
func (f *cleanupFixture) snapshot() string {
	f.t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(f.repo.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(f.repo.Dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h.Write([]byte(filepath.ToSlash(rel) + "\x00" + info.Mode().String() + "\x00"))
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(data)
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	h.Write([]byte(f.repo.Git("for-each-ref")))
	h.Write([]byte(f.repo.Git("worktree", "list", "--porcelain")))
	return hex.EncodeToString(h.Sum(nil))
}

// scannedDetectors returns the detectors a verbose run reported on, in the
// order of the per-detector summary lines ("  name: N finding(s) in ...").
func scannedDetectors(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if ok && strings.Contains(rest, "finding(s) in") {
			out = append(out, name)
		}
	}
	return out
}

func TestSweepDryRunChangesNothing(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	before := f.snapshot()
	code, out, errOut := brooom(t, "", "sweep")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"feat/merged", "feat/squash", "re-run 'brooom sweep --apply'"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if after := f.snapshot(); after != before {
		t.Error("a dry run changed the repository")
	}
	if got := f.sessions(); len(got) != 0 {
		t.Errorf("dry run wrote %d session(s)", len(got))
	}
}

func TestSweepApplyConfirmsAndWritesManifest(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()

	code, _, _ := brooom(t, "", "sweep", "--apply")
	if code != ExitUsage {
		t.Fatalf("--apply without confirmation: code %d, want %d", code, ExitUsage)
	}
	if !f.hasBranch("feat/merged") || len(f.sessions()) != 0 {
		t.Fatal("an unconfirmed apply changed something")
	}

	code, out, errOut := brooom(t, "", append([]string{"sweep", "--apply", "--yes"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if f.hasBranch("feat/merged") || f.hasBranch("feat/squash") {
		t.Errorf("merged branches remain: %v", f.branches())
	}
	ms := f.sessions()
	if len(ms) != 1 {
		t.Fatalf("want 1 session, got %d", len(ms))
	}
	if !strings.HasPrefix(ms[0].Command, "brooom sweep --apply --yes") {
		t.Errorf("manifest command %q", ms[0].Command)
	}
}

func TestSweepPresetSelection(t *testing.T) {
	standardCfg := map[string]any{"sweep": map[string]any{"preset": "standard"}}
	tests := []struct {
		name     string
		cfg      map[string]any
		args     []string
		want     []string // detectors that must be scanned
		wantNone []string // detectors that must not be scanned
	}{
		{"no flag, no config uses safe", nil, nil,
			[]string{config.DetectorMergedBranch, config.DetectorWorktrees}, []string{config.DetectorStaleBranch, config.DetectorGitBloat}},
		{"config default", standardCfg, nil,
			[]string{config.DetectorMergedBranch, config.DetectorStaleBranch, config.DetectorAIArtifacts}, []string{config.DetectorGitBloat}},
		{"flag overrides config", standardCfg, []string{"--preset", "safe"},
			[]string{config.DetectorMergedBranch}, []string{config.DetectorStaleBranch}},
		{"flag selects aggressive", nil, []string{"-p", "aggressive"},
			[]string{config.DetectorStaleBranch, config.DetectorGitBloat, config.DetectorLargeUntracked}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCleanupFixture(t, tt.cfg)
			f.mergedAndSquashed()
			code, _, errOut := brooom(t, "", append([]string{"sweep", "--verbose"}, tt.args...)...)
			if code != ExitOK {
				t.Fatalf("code %d, stderr %q", code, errOut)
			}
			got := scannedDetectors(errOut)
			for _, d := range tt.want {
				if !slices.Contains(got, d) {
					t.Errorf("%s was not scanned: %v", d, got)
				}
			}
			for _, d := range tt.wantNone {
				if slices.Contains(got, d) {
					t.Errorf("%s was scanned: %v", d, got)
				}
			}
		})
	}
}

func TestSweepUnknownPreset(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, _, errOut := brooom(t, "", "sweep", "--preset", "reckless")
	if code != ExitUsage {
		t.Fatalf("code %d, want %d; stderr %q", code, ExitUsage, errOut)
	}
	for _, want := range []string{"reckless", "safe", "standard", "aggressive"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("error does not name %q: %q", want, errOut)
		}
	}
}

func TestSweepInvalidConfigPresetIsRejected(t *testing.T) {
	newCleanupFixture(t, map[string]any{"sweep": map[string]any{"preset": "reckless"}})
	code, _, errOut := brooom(t, "", "sweep")
	if code == ExitOK || !strings.Contains(errOut, "sweep.preset") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestSweepDetectorNarrowsPreset(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, _, errOut := brooom(t, "", "sweep", "--preset", "standard", "--detector", "merged-branch", "--verbose")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if got := scannedDetectors(errOut); !slices.Equal(got, []string{config.DetectorMergedBranch}) {
		t.Errorf("scanned %v, want only merged-branch", got)
	}
}

func TestSweepDetectorOutsidePresetIsUsageError(t *testing.T) {
	newCleanupFixture(t, nil)
	tests := []struct {
		args    []string
		include string
	}{
		{[]string{"--detector", "stale-branch"}, "standard preset includes it"},
		{[]string{"--detector", "git-bloat"}, "aggressive preset includes it"},
		{[]string{"--preset", "standard", "--detector", "large-untracked"}, "aggressive preset includes it"},
	}
	for _, tt := range tests {
		code, _, errOut := brooom(t, "", append([]string{"sweep"}, tt.args...)...)
		if code != ExitUsage || !strings.Contains(errOut, tt.include) {
			t.Errorf("%v: code %d, stderr %q", tt.args, code, errOut)
		}
	}
	code, _, errOut := brooom(t, "", "sweep", "--detector", "no-such-detector")
	if code != ExitUsage || !strings.Contains(errOut, "unknown detector") {
		t.Errorf("typo: code %d, stderr %q", code, errOut)
	}
}

// TestSweepPresetDetectorsAreAllRegistered pins the assumption behind #238:
// every detector a preset names is linked into the binary, so sweep needs no
// "not available in this build" path. A preset naming a detector that does not
// exist fails here instead of being skipped quietly at run time.
func TestSweepPresetDetectorsAreAllRegistered(t *testing.T) {
	for _, n := range presets.Names() {
		p, _ := presets.Get(n)
		for _, d := range p.Detectors {
			if !registered(d) {
				t.Errorf("preset %s names %q, which is not registered", n, d)
			}
		}
	}
}

// TestSweepUnknownDetectorIsUsageError covers what replaced the unavailable
// branch: a --detector that no detector carries is a typo and exits 2.
func TestSweepUnknownDetectorIsUsageError(t *testing.T) {
	newCleanupFixture(t, nil)
	code, _, errOut := brooom(t, "", "sweep", "--preset", "aggressive", "--detector", "no-such-detector")
	if code != ExitUsage || !strings.Contains(errOut, `unknown detector "no-such-detector"`) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

// TestSweepBlockedFindingsAreNeverPlanned builds a merged branch that is
// checked out in a dirty worktree and an old branch with unpushed commits, then
// sweeps with the widest preset. Neither may be touched, with or without
// confirmation.
func TestSweepBlockedFindingsAreNeverPlanned(t *testing.T) {
	cfg := map[string]any{"detectors": map[string]any{
		"stale-branch": map[string]any{"enabled": true, "min_age_days": 1, "include_unpushed": true},
	}}
	f := newCleanupFixture(t, cfg)
	f.feature("feat/dirty")
	wt := f.worktreeInRepo("dirty", "feat/dirty")
	f.mergeCommit("feat/dirty")
	f.feature("feat/unmerged")
	f.publish()
	testutil.WriteFile(t, wt, "scratch.txt", "uncommitted\n")

	code, out, errOut := brooom(t, "", append([]string{"sweep", "--preset", "aggressive", "--apply", "--yes"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if _, err := os.Stat(filepath.Join(wt, "scratch.txt")); err != nil {
		t.Errorf("dirty worktree content lost: %v\n%s", err, out)
	}
	if !f.hasBranch("feat/unmerged") {
		t.Errorf("an unmerged branch was deleted: %v\n%s", f.branches(), out)
	}
	if !f.hasBranch("feat/dirty") {
		t.Errorf("the branch of the dirty worktree was deleted: %v\n%s", f.branches(), out)
	}
}

func TestSweepMachineFormatFiltersConfidence(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", "sweep", "--format", "json")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	var report findings.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("stdout is not a report: %v\n%s", err, out)
	}
	if len(report.Findings) == 0 {
		t.Fatal("no findings in the report")
	}
	for _, fi := range report.Findings {
		if fi.Confidence.Rank() < findings.ConfidenceHigh.Rank() {
			t.Errorf("%s has confidence %s below the safe floor", fi.ID, fi.Confidence)
		}
	}
	code, _, _ = brooom(t, "", "sweep", "--format", "json", "--apply", "--yes")
	if code != ExitUsage {
		t.Errorf("machine format with --apply: code %d, want %d", code, ExitUsage)
	}
}

func TestScanOptionsConfidenceFilter(t *testing.T) {
	mk := func(c findings.Confidence, flags ...findings.RiskFlag) findings.Finding {
		return findings.Finding{ID: string(c), Confidence: c, RiskFlags: flags}
	}
	in := []findings.Finding{
		mk(findings.ConfidenceHigh),
		mk(findings.ConfidenceMedium),
		mk(findings.ConfidenceLow),
		mk(findings.ConfidenceHigh, findings.RiskWorktreeDirty),
		mk(""),
	}
	tests := []struct {
		floor findings.Confidence
		want  int
	}{
		{"", 5},
		{findings.ConfidenceLow, 4},
		{findings.ConfidenceMedium, 3},
		{findings.ConfidenceHigh, 2},
	}
	for _, tt := range tests {
		got := scanOptions{minConfidence: tt.floor}.filter(in)
		if len(got) != tt.want {
			t.Errorf("floor %q kept %d, want %d", tt.floor, len(got), tt.want)
		}
	}

	// A blocked finding of sufficient confidence stays so the executor can
	// report it as blocked; the filter never hides blocking flags.
	kept := scanOptions{minConfidence: findings.ConfidenceHigh}.filter(in)
	if !slices.ContainsFunc(kept, func(f findings.Finding) bool { return len(f.RiskFlags) > 0 }) {
		t.Error("the blocked finding was dropped by the confidence filter")
	}

	var streamed []string
	cb := scanOptions{minConfidence: findings.ConfidenceMedium}.filterStream(func(f findings.Finding) { streamed = append(streamed, f.ID) })
	for _, f := range in {
		cb(f)
	}
	if want := []string{"high", "medium", "high"}; !slices.Equal(streamed, want) {
		t.Errorf("streamed %v, want %v", streamed, want)
	}
	if (scanOptions{minConfidence: findings.ConfidenceMedium}).filterStream(nil) != nil {
		t.Error("a nil callback must stay nil")
	}
}

// TestSweepOverlayReachesTheScanConfig checks that the overlay shapes the
// config the scan uses without rewriting the config file, and that it sits
// below .brooom.json: ForTarget still tightens the preset-lowered value.
func TestSweepOverlayReachesTheScanConfig(t *testing.T) {
	home := isolate(t)
	path := writeConfig(t, home, map[string]any{"thresholds": map[string]any{"min_age_days": 30}})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := presets.Get(presets.Aggressive)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{}
	req, err := a.newScanRequest(scanOptions{configOverlay: func(c *config.Config) { *c = *presets.Apply(c, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.cfg.Thresholds.MinAgeDays; got != presets.AggressiveAges.MinAgeDays {
		t.Errorf("overlay not applied: min_age_days %d", got)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("the config file was rewritten")
	}

	dir := t.TempDir()
	testutil.WriteFile(t, dir, config.RepoConfigFileName, `{"thresholds": {"min_age_days": 21}}`)
	eff, err := req.cfg.ForTarget("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Thresholds.MinAgeDays != 21 {
		t.Errorf(".brooom.json did not tighten the preset value: %d", eff.Thresholds.MinAgeDays)
	}
}

// registered reports whether the build links the named detector.
func registered(name string) bool {
	_, ok := detect.Get(name)
	return ok
}

func TestSweepHelpIsGeneratedFromPresets(t *testing.T) {
	newCleanupFixture(t, nil)
	code, out, errOut := brooom(t, "", "sweep", "--help")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, name := range presets.Names() {
		p, err := presets.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, name+": "+p.Summary) {
			t.Errorf("help lacks the summary of %s", name)
		}
		for _, d := range p.Detectors {
			if !strings.Contains(out, d) {
				t.Errorf("help of %s lacks detector %s", name, d)
			}
		}
	}
	if !strings.Contains(out, "safe, standard, aggressive") {
		t.Errorf("flag usage does not list the presets:\n%s", out)
	}
}

// TestSweepEmptyPresetIsUsageError pins that an explicitly empty --preset is
// not silently replaced by the default or by sweep.preset.
func TestSweepEmptyPresetIsUsageError(t *testing.T) {
	for _, value := range []string{"", "  "} {
		newCleanupFixture(t, map[string]any{"sweep": map[string]any{"preset": "aggressive"}})
		code, _, errOut := brooom(t, "", "sweep", "--preset", value)
		if code != ExitUsage {
			t.Fatalf("--preset %q: code %d, want %d; stderr %q", value, code, ExitUsage, errOut)
		}
		for _, want := range []string{"safe", "standard", "aggressive"} {
			if !strings.Contains(errOut, want) {
				t.Errorf("--preset %q: error does not name %q: %q", value, want, errOut)
			}
		}
	}
}
