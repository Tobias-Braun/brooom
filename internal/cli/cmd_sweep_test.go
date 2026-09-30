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
	"time"

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
	code, out, errOut := brooom(t, "", "sweep", "--dry-run")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"feat/merged", "feat/squash", "$ git branch -d", "re-run without --dry-run"} {
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

// TestSweepYesPrintsTheBriefSummary: with --yes nobody reads a plan, so sweep
// acts right away, records a session for undo and prints a summary that ends
// with the reclaimed size and carries no per-item plan or git command.
func TestSweepYesPrintsTheBriefSummary(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()

	code, out, errOut := brooom(t, "", append([]string{"sweep", "--yes"}, quarantine...)...)
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
	for _, bad := range []string{"git ", "$ ", "recovery hints"} {
		if strings.Contains(out, bad) {
			t.Errorf("default output contains %q:\n%s", bad, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if last := lines[len(lines)-1]; last != "2 merged branches removed. 0 B reclaimed" {
		t.Errorf("last line %q\n%s", last, out)
	}
	if !strings.Contains(out, "undo: brooom undo "+ms[0].ID) {
		t.Errorf("no undo line:\n%s", out)
	}
}

// TestSweepConfirmedShowsPlanThenBriefSummary: a confirmed sweep shows the
// plan the question refers to and still ends with the one-line summary.
func TestSweepConfirmedShowsPlanThenBriefSummary(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := runApp(t, "y\n", true, time.Time{}, append([]string{"sweep"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	for _, want := range []string{"feat/merged", "Proceed with 2 items", "2 merged branches removed. 0 B reclaimed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestSweepVerboseShowsTheFullSummary: --verbose keeps the plan and the
// multi-line summary.
func TestSweepVerboseShowsTheFullSummary(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", append([]string{"sweep", "--yes", "--verbose"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	for _, want := range []string{"feat/merged", "$ git branch -d", "summary: 2 applied"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if f.hasBranch("feat/merged") {
		t.Error("--verbose did not apply")
	}
}

// TestSweepQuietPrintsOnlyFailures: a successful quiet sweep with --yes is
// silent.
func TestSweepQuietPrintsOnlyFailures(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", append([]string{"sweep", "-q", "--yes"}, quarantine...)...)
	if code != ExitOK || out != "" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if f.hasBranch("feat/merged") {
		t.Error("-q did not apply")
	}
}

func TestSweepNothingToClean(t *testing.T) {
	newCleanupFixture(t, nil)
	code, out, errOut := brooom(t, "", "sweep")
	if code != ExitOK || strings.TrimSpace(out) != "nothing to clean" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// TestSweepHasNoApplyFlag: acting is the default, so --apply is gone.
func TestSweepHasNoApplyFlag(t *testing.T) {
	newCleanupFixture(t, nil)
	code, _, errOut := brooom(t, "", "sweep", "--apply")
	if code != ExitUsage || !strings.Contains(errOut, "--apply") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestSweepPresetSelection(t *testing.T) {
	tidyCfg := map[string]any{"sweep": map[string]any{"preset": "tidy"}}
	tests := []struct {
		name     string
		cfg      map[string]any
		args     []string
		want     []string // detectors that must be scanned
		wantNone []string // detectors that must not be scanned
	}{
		{"no argument, no config uses everything", nil, nil,
			[]string{config.DetectorMergedBranch, config.DetectorWorktrees, config.DetectorGitBloat, config.DetectorBuildArtifacts},
			[]string{config.DetectorStaleBranch, config.DetectorLargeUntracked}},
		{"config default", tidyCfg, nil,
			[]string{config.DetectorLogs}, []string{config.DetectorMergedBranch}},
		{"argument overrides config", tidyCfg, []string{"after-agents"},
			[]string{config.DetectorMergedBranch, config.DetectorAIArtifacts}, []string{config.DetectorLogs}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCleanupFixture(t, tt.cfg)
			f.mergedAndSquashed()
			code, _, errOut := brooom(t, "", append([]string{"sweep", "--dry-run", "--verbose"}, tt.args...)...)
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

func TestSweepInvalidConfigPresetIsRejected(t *testing.T) {
	newCleanupFixture(t, map[string]any{"sweep": map[string]any{"preset": "reckless"}})
	code, _, errOut := brooom(t, "", "sweep", "--dry-run")
	if code == ExitOK || !strings.Contains(errOut, "sweep.preset") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

// TestSweepLegacyConfigPresetStillLoads: `config init` wrote "safe" into every
// config file, which must keep working.
func TestSweepLegacyConfigPresetStillLoads(t *testing.T) {
	newCleanupFixture(t, map[string]any{"sweep": map[string]any{"preset": "safe"}})
	code, _, errOut := brooom(t, "", "sweep", "--dry-run", "--verbose")
	if code != ExitOK || !strings.Contains(errOut, `running "everything"`) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
	if !slices.Contains(scannedDetectors(errOut), config.DetectorGitBloat) {
		t.Errorf("legacy safe did not run everything: %v", scannedDetectors(errOut))
	}
}

func TestSweepDetectorNarrowsPreset(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, _, errOut := brooom(t, "", "sweep", "--detector", "merged-branch", "--dry-run", "--verbose")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if got := scannedDetectors(errOut); !slices.Equal(got, []string{config.DetectorMergedBranch}) {
		t.Errorf("scanned %v, want only merged-branch", got)
	}
}

func TestSweepUnknownDetectorIsUsageError(t *testing.T) {
	newCleanupFixture(t, nil)
	code, _, errOut := brooom(t, "", "sweep", "--detector", "no-such-detector", "--dry-run")
	if code != ExitUsage || !strings.Contains(errOut, `unknown detector "no-such-detector"`) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

// TestSweepPresetDetectorsAreAllRegistered pins the assumption behind #238:
// every detector a preset names is linked into the binary, so sweep needs no
// "not available in this build" path.
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

// TestSweepBlockedFindingsAreNeverPlanned builds a merged branch that is
// checked out in a dirty worktree and an old branch with unpushed commits, then
// sweeps with the widest preset. Neither may be touched.
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

	code, out, errOut := brooom(t, "", append([]string{"sweep", "everything", "--yes"}, quarantine...)...)
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

// TestSweepFloorsBuildArtifactsOfActiveProjects: everything plans build
// artifacts only at high confidence, so node_modules of a project that is
// still being worked on (medium) stays.
func TestSweepFloorsBuildArtifactsOfActiveProjects(t *testing.T) {
	f := newCleanupFixture(t, nil)
	testutil.WriteFile(t, f.repo.Dir, "package.json", "{}\n")
	testutil.WriteFile(t, filepath.Join(f.repo.Dir, "node_modules", "x"), "index.js", "x\n")
	_, out, errOut := brooom(t, "", "scan", "-d", "build-artifacts", "--format", "json")
	var report findings.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("scan: %v\n%s\n%s", err, out, errOut)
	}
	if len(report.Findings) == 0 || report.Findings[0].Confidence == findings.ConfidenceHigh {
		t.Skipf("fixture does not produce a medium build artifact finding: %+v", report.Findings)
	}
	code, out, errOut := brooom(t, "", "sweep", "-d", "build-artifacts", "--format", "json")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Errorf("everything kept a medium build artifact: %+v", report.Findings)
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
		if fi.Confidence.Rank() < findings.ConfidenceMedium.Rank() {
			t.Errorf("%s has confidence %s below the preset floor", fi.ID, fi.Confidence)
		}
	}
	if !f.hasBranch("feat/merged") {
		t.Error("a machine-format sweep acted")
	}
}

func TestScanOptionsKeepFilter(t *testing.T) {
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
	floor := func(c findings.Confidence) func(findings.Finding) bool {
		return func(f findings.Finding) bool { return f.Confidence.Rank() >= c.Rank() }
	}
	if got := (scanOptions{}).filter(in); len(got) != 5 {
		t.Errorf("no filter kept %d, want 5", len(got))
	}
	if got := (scanOptions{keep: floor(findings.ConfidenceHigh)}).filter(in); len(got) != 2 {
		t.Errorf("high floor kept %d, want 2", len(got))
	}

	// A blocked finding of sufficient confidence stays so the executor can
	// report it as blocked; the filter never hides blocking flags.
	kept := scanOptions{keep: floor(findings.ConfidenceHigh)}.filter(in)
	if !slices.ContainsFunc(kept, func(f findings.Finding) bool { return len(f.RiskFlags) > 0 }) {
		t.Error("the blocked finding was dropped by the confidence filter")
	}

	var streamed []string
	cb := scanOptions{keep: floor(findings.ConfidenceMedium)}.filterStream(func(f findings.Finding) { streamed = append(streamed, f.ID) })
	for _, f := range in {
		cb(f)
	}
	if want := []string{"high", "medium", "high"}; !slices.Equal(streamed, want) {
		t.Errorf("streamed %v, want %v", streamed, want)
	}
	if (scanOptions{keep: floor(findings.ConfidenceMedium)}).filterStream(nil) != nil {
		t.Error("a nil callback must stay nil")
	}
}

// TestSweepOverlayReachesTheScanConfig checks that the overlay shapes the
// config the scan uses without rewriting the config file, and that it sits
// below .brooom.json.
func TestSweepOverlayReachesTheScanConfig(t *testing.T) {
	home := isolate(t)
	path := writeConfig(t, home, map[string]any{"detectors": map[string]any{"git-bloat": map[string]any{"reflog_expire": "365.days.ago"}}})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := presets.Get(presets.Everything)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{}
	req, err := a.newScanRequest(scanOptions{configOverlay: func(c *config.Config) { *c = *presets.Apply(c, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.cfg.Detectors.GitBloat.ReflogExpire; got != presets.Expiry {
		t.Errorf("overlay not applied: reflog_expire %q", got)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("the config file was rewritten")
	}

	dir := t.TempDir()
	testutil.WriteFile(t, dir, config.RepoConfigFileName, `{"thresholds": {"min_age_days": 21}}`)
	eff, err := req.cfg.ForTarget(dir)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Thresholds.MinAgeDays != 21 {
		t.Errorf(".brooom.json did not tighten: %d", eff.Thresholds.MinAgeDays)
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
}
