package largeuntracked_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/detectors/largeuntracked"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// minSize is the threshold tests run with so that files of a few KB count.
const (
	minSize = 2048
	bigSize = 4096
)

// now is the scan time; files written by tests carry real mtimes, which lie
// after it, so they count as recently modified unless a test sets an mtime.
var now = testutil.BaseTime.AddDate(0, 0, 100)

type harness struct {
	t    *testing.T
	repo *testutil.Repo
	env  *detect.Env
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := testutil.ResolvedTempDir(t)
	t.Setenv("BROOOM_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	repo := testutil.NewRepo(t)
	return withRepo(t, repo, runner)
}

func withRepo(t *testing.T, repo *testutil.Repo, runner gitx.Runner) *harness {
	t.Helper()
	g, err := scope.NewGuard(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Detectors.LargeUntracked.MinSizeBytes = minSize
	env := &detect.Env{Config: cfg, Git: runner, Guard: g, Now: now, CacheDir: testutil.ResolvedTempDir(t)}
	return &harness{t: t, repo: repo, env: env}
}

func (h *harness) cfg() *config.LargeUntracked { return &h.env.Config.Detectors.LargeUntracked }

func (h *harness) big(rel string) string {
	h.t.Helper()
	return h.repo.WriteFile(rel, strings.Repeat("x", bigSize))
}

func (h *harness) target(kind scope.TargetKind) scope.Target {
	return scope.Target{Kind: kind, Path: h.repo.Dir, Scope: findings.Scope{Type: findings.ScopeRepo, Path: h.repo.Dir}}
}

func (h *harness) run() []findings.Finding {
	h.t.Helper()
	return h.runTarget(h.target(scope.TargetRepo))
}

func (h *harness) runTarget(target scope.Target) []findings.Finding {
	h.t.Helper()
	var out []findings.Finding
	err := largeuntracked.New().Detect(context.Background(), h.env, target, func(f findings.Finding) { out = append(out, f) })
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

// byRel returns the findings keyed by their path relative to the repository.
func (h *harness) byRel(fs []findings.Finding) map[string]findings.Finding {
	h.t.Helper()
	m := map[string]findings.Finding{}
	for _, f := range fs {
		rel, err := filepath.Rel(h.repo.Dir, f.Path)
		if err != nil {
			h.t.Fatal(err)
		}
		m[filepath.ToSlash(rel)] = f
	}
	return m
}

func (h *harness) want(fs []findings.Finding, rels ...string) {
	h.t.Helper()
	var got []string
	for r := range h.byRel(fs) {
		got = append(got, r)
	}
	slices.Sort(got)
	want := slices.Clone(rels)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		h.t.Fatalf("findings for %v, want %v", got, want)
	}
}

func evidence(f findings.Finding, code string) (findings.Evidence, bool) {
	for _, e := range f.Evidence {
		if e.Code == code {
			return e, true
		}
	}
	return findings.Evidence{}, false
}

func TestRegistration(t *testing.T) {
	d, ok := detect.Get(largeuntracked.Name)
	if !ok {
		t.Fatal("large-untracked is not registered")
	}
	if d.Name() != "large-untracked" || d.Category() != detect.CategoryFiles || d.Description() == "" {
		t.Errorf("unexpected detector metadata: %q %q", d.Name(), d.Category())
	}
}

func TestUntrackedFile(t *testing.T) {
	h := newHarness(t)
	h.big("data/model.ckpt")
	h.repo.WriteFile("data/small.txt", "tiny")
	old := testutil.BaseTime
	testutil.SetMTime(t, filepath.Join(h.repo.Dir, "data", "model.ckpt"), old)

	fs := h.run()
	h.want(fs, "data/model.ckpt")
	f := fs[0]
	if f.Kind != findings.KindFile || f.SizeBytes != bigSize || f.Detector != "large-untracked" {
		t.Errorf("unexpected finding: %+v", f)
	}
	if f.ID != findings.NewID("large-untracked", findings.KindFile, f.Path, "") {
		t.Errorf("unexpected id %q", f.ID)
	}
	if f.AgeDays != 100 || f.LastModified == nil || !f.LastModified.Equal(old) {
		t.Errorf("age/mtime = %d %v", f.AgeDays, f.LastModified)
	}
	checkUntrackedSafety(t, f)
	checkUntrackedEvidence(t, f)
}

// checkUntrackedSafety asserts the safety contract of an untracked finding.
func checkUntrackedSafety(t *testing.T, f findings.Finding) {
	t.Helper()
	if f.Confidence != findings.ConfidenceLow {
		t.Errorf("confidence = %s, want low", f.Confidence)
	}
	if f.SuggestedAction.Type != findings.ActionTrash || !strings.Contains(f.SuggestedAction.Reason, "permanent deletion is refused") {
		t.Errorf("suggested action = %+v", f.SuggestedAction)
	}
	if f.Meta["user_data_risk"] != "untracked" {
		t.Errorf("meta = %v, want user_data_risk=untracked", f.Meta)
	}
	// uncommitted_changes would block the suggestion, see the package doc.
	if f.Blocked() || f.HasRisk(findings.RiskUncommittedChanges) || f.HasRisk(findings.RiskGitignored) || f.HasRisk(findings.RiskRecentlyModified) {
		t.Errorf("unexpected risk flags: %v", f.RiskFlags)
	}
}

func checkUntrackedEvidence(t *testing.T, f findings.Finding) {
	t.Helper()
	for _, code := range []string{"untracked_file", "size_over_threshold", "last_modified_age"} {
		if _, ok := evidence(f, code); !ok {
			t.Errorf("missing evidence %s", code)
		}
	}
	if e, _ := evidence(f, "size_over_threshold"); e.Value != int64(bigSize) {
		t.Errorf("size_over_threshold value = %v", e.Value)
	}
}

func TestEveryUntrackedFindingCarriesUserDataRisk(t *testing.T) {
	h := newHarness(t)
	h.repo.WriteFile(".gitignore", "*.ign\nignored_dir/\n")
	h.big("a.bin")
	h.big("b/c.bin")
	h.big("x.ign")
	h.big("ignored_dir/y.bin")
	for _, f := range h.run() {
		ignored := f.HasRisk(findings.RiskGitignored)
		if _, has := f.Meta["user_data_risk"]; has == ignored {
			t.Errorf("%s: ignored=%v meta=%v", f.Path, ignored, f.Meta)
		}
	}
}

func TestThresholdBoundary(t *testing.T) {
	h := newHarness(t)
	h.repo.WriteFile("exact.bin", strings.Repeat("x", minSize))
	h.repo.WriteFile("below.bin", strings.Repeat("x", minSize-1))
	h.want(h.run(), "exact.bin")
}

func TestZeroMinSizeMeansDefault(t *testing.T) {
	h := newHarness(t)
	h.cfg().MinSizeBytes = 0
	h.big("a.bin")
	if fs := h.run(); len(fs) != 0 {
		t.Fatalf("a zero threshold must fall back to 100 MiB, got %d findings", len(fs))
	}
	if largeuntracked.DefaultMinSizeBytes != 100<<20 {
		t.Errorf("default threshold = %d", largeuntracked.DefaultMinSizeBytes)
	}
}

func TestUnusualNames(t *testing.T) {
	names := []string{"with space.bin", "ünï/cødé — 名前.bin", "it's here.bin"}
	if runtime.GOOS != "windows" {
		// Double quotes and newlines are illegal in Windows file names.
		names = append(names, "quote\"s.bin", "line\nbreak.bin", "trailing\n")
	}
	h := newHarness(t)
	for _, n := range names {
		h.big(n)
	}
	// Non-ASCII names must not depend on git's quoting configuration.
	h.repo.Git("config", "core.quotepath", "true")
	h.want(h.run(), names...)
}

func TestIgnoredFilesAndDirs(t *testing.T) {
	h := newHarness(t)
	h.repo.WriteFile(".gitignore", "*.ckpt\ncache_dir/\n")
	h.big("weights.ckpt")
	h.big("cache_dir/one.bin")
	h.repo.WriteFile("cache_dir/two.bin", strings.Repeat("y", bigSize))
	h.repo.WriteFile("small_dir_ignored.ckpt", "x")
	h.repo.CommitAll("ignore", testutil.BaseTime)

	fs := h.run()
	h.want(fs, "weights.ckpt", "cache_dir")
	m := h.byRel(fs)
	file, dir := m["weights.ckpt"], m["cache_dir"]
	if file.Confidence != findings.ConfidenceMedium || !file.HasRisk(findings.RiskGitignored) || file.Meta != nil {
		t.Errorf("ignored file: %+v", file)
	}
	if file.SuggestedAction.Type != findings.ActionTrash {
		t.Errorf("ignored file action = %+v", file.SuggestedAction)
	}
	if _, ok := evidence(file, "ignored_by_git"); !ok {
		t.Error("missing ignored_by_git evidence")
	}
	if dir.Kind != findings.KindDir || dir.SizeBytes < 2*bigSize {
		t.Errorf("collapsed dir finding = kind %s size %d", dir.Kind, dir.SizeBytes)
	}
}

func TestIncludeIgnoredOff(t *testing.T) {
	h := newHarness(t)
	h.cfg().IncludeIgnored = false
	h.repo.WriteFile(".gitignore", "*.ckpt\ncache_dir/\n")
	h.big("weights.ckpt")
	h.big("cache_dir/one.bin")
	h.big("plain.bin")
	h.want(h.run(), "plain.bin")
}

// TestIgnoredDirSizedFresh proves the directory walk bypasses the cache: a
// second run after touching a file must see the new modification time even
// though the cache was populated by the first run.
func TestIgnoredDirSizedFresh(t *testing.T) {
	h := newHarness(t)
	h.repo.WriteFile(".gitignore", "cache_dir/\n")
	file := h.big("cache_dir/one.bin")
	old := testutil.BaseTime
	testutil.SetMTime(t, file, old)
	testutil.SetMTime(t, filepath.Dir(file), old)

	first := h.byRel(h.run())["cache_dir"]
	if first.LastModified == nil || !first.LastModified.Equal(old) || first.HasRisk(findings.RiskRecentlyModified) {
		t.Fatalf("first run: %+v", first)
	}

	// An in-place change does not touch the directory mtime, the case a stale
	// cache would miss.
	fresh := now.Add(-time.Hour)
	testutil.SetMTime(t, file, fresh)
	testutil.SetMTime(t, filepath.Dir(file), old)
	second := h.byRel(h.run())["cache_dir"]
	if second.LastModified == nil || !second.LastModified.Equal(fresh) || !second.HasRisk(findings.RiskRecentlyModified) {
		t.Fatalf("second run must reflect the new mtime: %+v", second)
	}
}

func TestClaimedDirsAreNotDuplicated(t *testing.T) {
	h := newHarness(t)
	h.repo.WriteFile(".gitignore", "node_modules/\n")
	h.big("node_modules/pkg/huge.bin")
	h.big("node_modules/pkg/other.bin")
	// Untracked but not ignored: git lists each file, claims must still apply.
	h.big("dist/bundle.js")
	h.big(".claude/run.log")
	h.big("keep/plain.bin")
	h.big(".claude/notes.bin")

	fs := h.run()
	h.want(fs, "keep/plain.bin", ".claude/notes.bin")
	for _, f := range fs {
		for _, claimed := range []string{"node_modules", "dist", "run.log"} {
			if strings.Contains(f.Path, claimed) {
				t.Errorf("finding %s lies inside a claimed path", f.Path)
			}
		}
	}
}

func TestClaimsOnlyApplyWhenOtherDetectorIsEnabled(t *testing.T) {
	h := newHarness(t)
	h.env.Config.Detectors.BuildArtifacts.Enabled = false
	h.big("dist/bundle.js")
	h.want(h.run(), "dist/bundle.js")
}

func TestExcludes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *harness)
	}{
		{"repo", func(h *harness) {
			// The exclude comes from the repository's own .brooom.json.
			h.repo.WriteFile(".brooom.json", `{"exclude": ["skipme"]}`)
		}},
		{"root", func(h *harness) {
			h.env.Config.Roots = []config.Root{{Path: filepath.Dir(h.repo.Dir), Exclude: []string{"**/skipme"}}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			h.big("skipme/deep/a.bin")
			h.big("kept/b.bin")
			h.repo.WriteFile(".gitignore", "ignoredskip/\n")
			h.want(h.run(), "kept/b.bin")
		})
	}
}

func TestExcludedIgnoredDir(t *testing.T) {
	h := newHarness(t)
	h.repo.WriteFile(".brooom.json", `{"exclude": ["ign"]}`)
	h.repo.WriteFile(".gitignore", "ign/\n")
	h.big("ign/a.bin")
	h.want(h.run())
}

func TestSymlinkSkipped(t *testing.T) {
	h := newHarness(t)
	target := h.big("real.bin")
	link := filepath.Join(h.repo.Dir, "link.bin")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("cannot create symlinks here:", err)
	}
	outside := testutil.ResolvedTempDir(t)
	testutil.WriteFile(t, outside, "secret.bin", strings.Repeat("z", bigSize))
	if err := os.Symlink(outside, filepath.Join(h.repo.Dir, "linkdir")); err != nil {
		t.Skip("cannot create directory symlinks here:", err)
	}
	h.want(h.run(), "real.bin")
}

func TestNonRegularFilesSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mkfifo is not available on Windows")
	}
	h := newHarness(t)
	if err := exec.Command("mkfifo", filepath.Join(h.repo.Dir, "pipe")).Run(); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	h.big("real.bin")
	h.want(h.run(), "real.bin")
}

func TestRecentlyModified(t *testing.T) {
	h := newHarness(t)
	recent := h.big("recent.bin")
	stale := h.big("stale.bin")
	testutil.SetMTime(t, recent, now.Add(-24*time.Hour))
	testutil.SetMTime(t, stale, now.AddDate(0, 0, -60))
	m := h.byRel(h.run())
	if !m["recent.bin"].HasRisk(findings.RiskRecentlyModified) {
		t.Error("recent.bin must be flagged recently_modified")
	}
	if m["stale.bin"].HasRisk(findings.RiskRecentlyModified) {
		t.Error("stale.bin must not be flagged")
	}
	if m["recent.bin"].SuggestedAction.Type != findings.ActionTrash {
		t.Error("recently modified alone must not block")
	}
}

func TestNestedRepoAndWorktreeSkipped(t *testing.T) {
	h := newHarness(t)
	nested := filepath.Join(h.repo.Dir, "vendor", "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", nested, "init", "-q")
	cmd.Env = h.repo.Env(testutil.BaseTime)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skip("git init failed:", err, string(out))
	}
	h.big("vendor/sub/inner.bin")
	h.repo.WriteFile(".gitignore", "ignored_repo/\n")
	h.big("ignored_repo/x.bin")
	if err := os.Mkdir(filepath.Join(h.repo.Dir, "ignored_repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.big("outer.bin")
	h.want(h.run(), "outer.bin")
}

func TestRepoWithoutCommits(t *testing.T) {
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	repo := testutil.NewRepo(t)
	empty := testutil.ResolvedTempDir(t)
	cmd := exec.Command("git", "-C", empty, "init", "-q")
	cmd.Env = repo.Env(testutil.BaseTime)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	repo.Dir = empty
	h := withRepo(t, repo, runner)
	h.big("first.bin")
	h.want(h.run(), "first.bin")
}

func TestManyUntrackedFiles(t *testing.T) {
	h := newHarness(t)
	for i := range 1500 {
		h.repo.WriteFile(fmt.Sprintf("many/d%d/f%d.txt", i%20, i), "s")
	}
	h.big("many/d3/big.bin")
	h.want(h.run(), "many/d3/big.bin")
}

func TestNonRepoTargetsAreSkipped(t *testing.T) {
	h := newHarness(t)
	h.big("a.bin")
	for _, kind := range []scope.TargetKind{scope.TargetProject, scope.TargetUser} {
		if fs := h.runTarget(h.target(kind)); len(fs) != 0 {
			t.Errorf("%s target produced findings", kind)
		}
	}
}

func TestDisabled(t *testing.T) {
	h := newHarness(t)
	h.cfg().Enabled = false
	h.big("a.bin")
	h.want(h.run())
}

func TestPathsOutsideGuardAreDropped(t *testing.T) {
	h := newHarness(t)
	h.big("a.bin")
	// A guard that no longer allows the repository refuses every entry
	// silently instead of failing the scan.
	other := testutil.ResolvedTempDir(t)
	g, err := scope.NewGuard(other)
	if err != nil {
		t.Fatal(err)
	}
	h.env.Guard = g
	h.want(h.run())
}

func TestOpenFileChecks(t *testing.T) {
	tests := []struct {
		name      string
		result    func(paths []string) map[string]bool
		err       error
		wantOpen  bool
		wantUnavl bool
	}{
		{"none open", func(p []string) map[string]bool { return map[string]bool{p[0]: false} }, nil, false, false},
		{"open", func(p []string) map[string]bool { return map[string]bool{p[0]: true} }, nil, true, false},
		{"unavailable", func([]string) map[string]bool { return nil }, procs.ErrUnavailable, false, true},
		{"incomplete false entry", func(p []string) map[string]bool { return map[string]bool{p[0]: false} }, procs.ErrIncomplete, false, true},
		{"incomplete true entry", func(p []string) map[string]bool { return map[string]bool{p[0]: true} }, procs.ErrIncomplete, true, false},
		{"other error is unknown", func([]string) map[string]bool { return nil }, errors.New("boom"), false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.big("a.bin")
			calls := 0
			restore := largeuntracked.SetOpenFiles(func(_ context.Context, paths []string) (map[string]bool, error) {
				calls++
				return tc.result(paths), tc.err
			})
			defer restore()
			fs := h.run()
			if len(fs) != 1 || calls != 1 {
				t.Fatalf("findings=%d calls=%d", len(fs), calls)
			}
			f := fs[0]
			if f.HasRisk(findings.RiskFileOpen) != tc.wantOpen {
				t.Errorf("file_open flag = %v, want %v", f.HasRisk(findings.RiskFileOpen), tc.wantOpen)
			}
			_, unavl := evidence(f, "open_check_unavailable")
			if unavl != tc.wantUnavl {
				t.Errorf("open_check_unavailable = %v, want %v", unavl, tc.wantUnavl)
			}
			if tc.wantOpen {
				if f.SuggestedAction.Type != findings.ActionNone || f.SuggestedAction.Reason == "" || !f.Blocked() {
					t.Errorf("open file must block: %+v", f)
				}
			} else if f.SuggestedAction.Type != findings.ActionTrash {
				t.Errorf("action = %+v", f.SuggestedAction)
			}
		})
	}
}

func TestOpenLookupIsBatched(t *testing.T) {
	h := newHarness(t)
	for i := range 5 {
		h.big(fmt.Sprintf("f%d.bin", i))
	}
	calls, seen := 0, 0
	restore := largeuntracked.SetOpenFiles(func(_ context.Context, paths []string) (map[string]bool, error) {
		calls++
		seen = len(paths)
		return map[string]bool{}, nil
	})
	defer restore()
	h.run()
	if calls != 1 || seen != 5 {
		t.Errorf("calls=%d paths=%d, want one call with 5 paths", calls, seen)
	}
}

func TestNoOpenLookupWithoutCandidates(t *testing.T) {
	h := newHarness(t)
	restore := largeuntracked.SetOpenFiles(func(context.Context, []string) (map[string]bool, error) {
		t.Error("OpenFiles must not be called without candidates")
		return nil, nil
	})
	defer restore()
	h.run()
}

func TestContextCancelled(t *testing.T) {
	h := newHarness(t)
	h.big("a.bin")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := largeuntracked.New().Detect(ctx, h.env, h.target(scope.TargetRepo), func(findings.Finding) {})
	if err == nil {
		t.Fatal("a cancelled context must return an error")
	}
}

func TestFindingsAreJSONSerialisable(t *testing.T) {
	h := newHarness(t)
	h.big("a.bin")
	if _, err := json.Marshal(h.run()); err != nil {
		t.Fatal(err)
	}
}
