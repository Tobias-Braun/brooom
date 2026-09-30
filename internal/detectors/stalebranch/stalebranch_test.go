package stalebranch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// fixture is a repository with a bare origin, a fixed scan time and a
// configuration the tests tweak before running the detector.
type fixture struct {
	t      *testing.T
	repo   *testutil.Repo
	runner gitx.Runner
	now    time.Time
	cfg    *config.Config
	gh     gitx.GHRunner
	force  bool
	// extraAllowed are additional guard locations (linked worktrees).
	extraAllowed []string
}

func newFixture(t *testing.T, withRemote bool) *fixture {
	t.Helper()
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	// Isolate the runner from the developer's git configuration.
	home := testutil.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BROOOM_HOME", filepath.Join(home, ".brooom"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig-none"))
	f := &fixture{t: t, runner: runner, now: testutil.BaseTime.AddDate(0, 0, 200), cfg: config.Default()}
	if withRemote {
		f.repo = testutil.NewRepoWithRemote(t)
	} else {
		f.repo = testutil.NewRepo(t)
	}
	// Tests without gh injection must never spawn the real gh.
	f.cfg.Git.UseGH = false
	return f
}

// daysAgo returns the time n whole days before the scan time.
func (f *fixture) daysAgo(n int) time.Time { return f.now.Add(-time.Duration(n) * 24 * time.Hour) }

// branch creates a branch off main with one commit at when; main stays
// checked out.
func (f *fixture) branch(name string, when time.Time) {
	f.t.Helper()
	f.repo.Git("checkout", "-q", "-b", name, "main")
	f.repo.Commit(name+".txt", name, "work on "+name, when)
	f.repo.Checkout("main")
}

// pushed creates a branch and pushes it with upstream tracking.
func (f *fixture) pushed(name string, when time.Time) {
	f.t.Helper()
	f.branch(name, when)
	f.repo.Push(name)
}

func (f *fixture) detect(target string) ([]findings.Finding, error) {
	f.t.Helper()
	allowed := append([]string{f.repo.Dir}, f.extraAllowed...)
	guard, err := scope.NewGuard(allowed...)
	if err != nil {
		f.t.Fatal(err)
	}
	env := &detect.Env{Config: f.cfg, Git: f.runner, Repos: gitx.NewCache(f.runner), Guard: guard, Now: f.now, Force: f.force}
	return f.detectWith(env, target)
}

func (f *fixture) detectWith(env *detect.Env, target string) ([]findings.Finding, error) {
	f.t.Helper()
	tgt := scope.Target{Kind: scope.TargetRepo, Path: target, Scope: findings.Scope{Type: findings.ScopeRepo, Path: target}}
	var out []findings.Finding
	err := (&Detector{GH: f.gh}).Detect(context.Background(), env, tgt, func(x findings.Finding) { out = append(out, x) })
	return out, err
}

func (f *fixture) mustDetect() []findings.Finding {
	f.t.Helper()
	out, err := f.detect(f.repo.Dir)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// only returns the single finding of a run.
func (f *fixture) only() findings.Finding {
	f.t.Helper()
	out := f.mustDetect()
	if len(out) != 1 {
		f.t.Fatalf("want exactly one finding, got %d: %+v", len(out), out)
	}
	return out[0]
}

// want is the complete expectation for one finding.
type want struct {
	ref        string
	confidence findings.Confidence
	flags      []findings.RiskFlag
	evidence   []string
	action     findings.ActionType
	command    string
	verified   string
	reason     string
	prCheck    string
	age        int
}

func (f *fixture) check(got findings.Finding, w want) {
	f.t.Helper()
	f.checkIdentity(got, w)
	f.checkSignals(got, w)
	f.checkAction(got, w)
	if got.Meta["tip"] == "" || got.Meta["open_pr_check"] != w.prCheck {
		f.t.Errorf("meta %v want open_pr_check %q", got.Meta, w.prCheck)
	}
	if got.HasRisk(findings.RiskRecentlyModified) {
		f.t.Error("recently_modified must never be set")
	}
}

func (f *fixture) checkIdentity(got findings.Finding, w want) {
	f.t.Helper()
	if got.Detector != Name || got.Kind != findings.KindBranch || got.Ref != w.ref {
		f.t.Errorf("identity: %s %s %q", got.Detector, got.Kind, got.Ref)
	}
	if got.Path != f.repo.Dir || got.Scope.Path != f.repo.Dir || got.Scope.Type != findings.ScopeRepo {
		f.t.Errorf("path/scope: %q %+v want %q", got.Path, got.Scope, f.repo.Dir)
	}
	if got.ID != findings.NewID(Name, findings.KindBranch, f.repo.Dir, w.ref) {
		f.t.Errorf("id %q", got.ID)
	}
	if got.SizeBytes != 0 || got.LastModified == nil || got.AgeDays != w.age {
		f.t.Errorf("size %d, lastmod %v, age %d want %d", got.SizeBytes, got.LastModified, got.AgeDays, w.age)
	}
}

func (f *fixture) checkSignals(got findings.Finding, w want) {
	f.t.Helper()
	if got.Confidence != w.confidence {
		f.t.Errorf("confidence %s want %s", got.Confidence, w.confidence)
	}
	if !reflect.DeepEqual(got.RiskFlags, w.flags) {
		f.t.Errorf("flags %v want %v", got.RiskFlags, w.flags)
	}
	var codes []string
	for _, e := range got.Evidence {
		codes = append(codes, e.Code)
	}
	if !reflect.DeepEqual(codes, w.evidence) {
		f.t.Errorf("evidence %v want %v", codes, w.evidence)
	}
}

func (f *fixture) checkAction(got findings.Finding, w want) {
	f.t.Helper()
	sa := got.SuggestedAction
	if sa.Type != w.action || sa.Command != w.command || sa.Args["verified"] != w.verified {
		f.t.Errorf("action %+v want %+v", sa, w)
	}
	if w.reason != "" && sa.Reason != w.reason {
		f.t.Errorf("reason %q want %q", sa.Reason, w.reason)
	}
	if sa.Reason == "" {
		f.t.Error("reason must explain the action")
	}
	if got.Blocked() && sa.Type != findings.ActionNone {
		f.t.Error("blocked finding suggests an action without force")
	}
}

const unpushedReason = "1 commits exist on no remote; deleting would lose them (re-run with --force to override)"

func TestUpstreamGoneContainedIsHigh(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/gone", f.daysAgo(100))
	// keep holds the commits after the branch's own remote ref is deleted.
	f.repo.Git("push", "-q", "origin", "feat/gone:keep")
	f.repo.DeleteRemoteBranch("feat/gone")
	f.repo.Fetch()
	f.check(f.only(), want{
		ref: "feat/gone", confidence: findings.ConfidenceHigh, age: 100,
		flags:    []findings.RiskFlag{findings.RiskUpstreamGone},
		evidence: []string{"last_commit_age", "upstream_gone", "contained_in_remote"},
		action:   findings.ActionDeleteBranch, command: "git branch -D feat/gone", verified: "in-remote",
		prCheck: prCheckDisabled,
	})
}

func TestUpstreamPresentContainedIsMediumWithSafeDelete(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/kept", f.daysAgo(120))
	got := f.only()
	f.check(got, want{
		ref: "feat/kept", confidence: findings.ConfidenceMedium, age: 120,
		evidence: []string{"last_commit_age", "contained_in_remote"},
		action:   findings.ActionDeleteBranch, command: "git branch -d feat/kept", verified: "in-remote",
		prCheck: prCheckDisabled, flags: []findings.RiskFlag{},
	})
	if got.Meta["upstream"] != "origin/feat/kept" || got.Meta["base"] != "origin/main" {
		t.Errorf("meta %v", got.Meta)
	}
	if got.Evidence[1].Value != "origin/feat/kept" {
		t.Errorf("contained_in_remote value %v", got.Evidence[1].Value)
	}
}

func TestRemoteTrackingTwinIsNotNeverPushed(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/twin", f.daysAgo(100))
	f.repo.Git("branch", "--unset-upstream", "feat/twin")
	f.check(f.only(), want{
		ref: "feat/twin", confidence: findings.ConfidenceMedium, age: 100,
		evidence: []string{"last_commit_age", "contained_in_remote"},
		action:   findings.ActionDeleteBranch, command: "git branch -D feat/twin", verified: "in-remote",
		prCheck: prCheckDisabled, flags: []findings.RiskFlag{},
	})
}

func TestNeverPushedLocalCommitsAreBlocked(t *testing.T) {
	f := newFixture(t, true)
	f.branch("feat/local", f.daysAgo(100))
	f.check(f.only(), want{
		ref: "feat/local", confidence: findings.ConfidenceLow, age: 100,
		flags:    []findings.RiskFlag{findings.RiskUnpushedCommits, findings.RiskNeverPushed},
		evidence: []string{"last_commit_age", "never_pushed", "unpushed_commits"},
		action:   findings.ActionNone, reason: unpushedReason, prCheck: prCheckDisabled,
	})
}

func TestUnpushedAheadOfUpstreamIsBlockedNotMedium(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/ahead", f.daysAgo(150))
	f.repo.Git("checkout", "-q", "feat/ahead")
	f.repo.Commit("more.txt", "x", "unpushed", f.daysAgo(100))
	f.repo.Checkout("main")
	f.check(f.only(), want{
		ref: "feat/ahead", confidence: findings.ConfidenceLow, age: 100,
		flags:    []findings.RiskFlag{findings.RiskUnpushedCommits},
		evidence: []string{"last_commit_age", "unpushed_commits"},
		action:   findings.ActionNone, reason: unpushedReason, prCheck: prCheckDisabled,
	})
}

func TestIncludeUnpushedFalseOmitsBranch(t *testing.T) {
	for _, remote := range []bool{true, false} {
		f := newFixture(t, remote)
		f.branch("feat/local", f.daysAgo(100))
		f.cfg.Detectors.StaleBranch.IncludeUnpushed = false
		if out := f.mustDetect(); len(out) != 0 {
			t.Errorf("remote=%v: want no findings, got %+v", remote, out)
		}
	}
}

func TestNeverPushedButContainedIsLow(t *testing.T) {
	f := newFixture(t, true)
	f.branch("feat/pointer", f.daysAgo(100))
	// Another remote branch holds the commits, but this one has no upstream
	// and no remote-tracking twin.
	f.repo.Git("push", "-q", "origin", "feat/pointer:keep")
	f.check(f.only(), want{
		ref: "feat/pointer", confidence: findings.ConfidenceLow, age: 100,
		flags:    []findings.RiskFlag{findings.RiskNeverPushed},
		evidence: []string{"last_commit_age", "never_pushed", "contained_in_remote"},
		action:   findings.ActionDeleteBranch, command: "git branch -D feat/pointer", verified: "in-remote",
		prCheck: prCheckDisabled,
	})
}

func TestRepoWithoutRemote(t *testing.T) {
	f := newFixture(t, false)
	f.branch("feat/local", f.daysAgo(100))
	got := f.only()
	f.check(got, want{
		ref: "feat/local", confidence: findings.ConfidenceLow, age: 100,
		flags:    []findings.RiskFlag{findings.RiskUnpushedCommits, findings.RiskNeverPushed},
		evidence: []string{"last_commit_age", "never_pushed", "unpushed_commits"},
		action:   findings.ActionNone, reason: "2 commits exist on no remote; deleting would lose them (re-run with --force to override)",
		prCheck: prCheckDisabled,
	})
	if got.Meta["base"] != "main" {
		t.Errorf("local base expected, meta %v", got.Meta)
	}
}

func TestAgeThreshold(t *testing.T) {
	tests := []struct {
		name string
		min  int
		when func(f *fixture) time.Time
		want bool
	}{
		{"exactly at threshold", 90, func(f *fixture) time.Time { return f.daysAgo(90) }, true},
		{"one day younger", 90, func(f *fixture) time.Time { return f.daysAgo(89) }, false},
		{"just under a full day short", 90, func(f *fixture) time.Time { return f.daysAgo(90).Add(time.Hour) }, false},
		{"zero makes everything stale", 0, func(f *fixture) time.Time { return f.now }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.cfg.Detectors.StaleBranch.MinAgeDays = tt.min
			f.pushed("feat/x", tt.when(f))
			if got := len(f.mustDetect()) == 1; got != tt.want {
				t.Errorf("reported = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPerRepoThresholdOverride(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/x", f.daysAgo(100))
	if len(f.mustDetect()) != 1 {
		t.Fatal("precondition: 100 day old branch is stale at 90 days")
	}
	testutil.WriteFile(t, f.repo.Dir, ".brooom.json", `{"thresholds":{"min_age_days":120}}`)
	if out := f.mustDetect(); len(out) != 0 {
		t.Errorf("tightened repo threshold must hide the branch, got %+v", out)
	}
}

func TestOpenPullRequest(t *testing.T) {
	ghOK := func(_ context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
		return []byte(`[{"headRefName":"feat/pr"}]`), nil
	}
	ghFail := func(_ context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
		return nil, errors.New("offline")
	}
	tests := []struct {
		name string
		gh   gitx.GHRunner
		want want
	}{
		{"open pr blocks", ghOK, want{
			confidence: findings.ConfidenceLow,
			flags:      []findings.RiskFlag{findings.RiskHasOpenPR},
			action:     findings.ActionNone, prCheck: prCheckOK,
			reason: "an open pull request uses this branch (re-run with --force to override)",
		}},
		{"unknown does not block", ghFail, want{
			confidence: findings.ConfidenceMedium, flags: []findings.RiskFlag{},
			action: findings.ActionDeleteBranch, command: "git branch -d feat/pr", verified: "in-remote", prCheck: prCheckUnknown,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.cfg.Git.UseGH = true
			f.gh = tt.gh
			f.pushed("feat/pr", f.daysAgo(100))
			tt.want.ref, tt.want.age = "feat/pr", 100
			tt.want.evidence = []string{"last_commit_age", "contained_in_remote"}
			f.check(f.only(), tt.want)
		})
	}
}

func TestMergedBranchesAreSkipped(t *testing.T) {
	f := newFixture(t, true)
	f.branch("merged/ancestor", f.daysAgo(100))
	f.branch("merged/squash", f.daysAgo(101))
	f.branch("merged/rebase", f.daysAgo(102))
	f.repo.Git("branch", "merged/history", "main")
	f.repo.Checkout("main")
	f.repo.Git("merge", "-q", "--ff-only", "merged/ancestor")
	f.repo.SquashMerge("merged/squash", "squash", f.daysAgo(50))
	f.repo.RebaseMerge("merged/rebase", f.daysAgo(49))
	f.repo.Git("push", "-q", "origin", "main")
	f.repo.Fetch()
	if out := f.mustDetect(); len(out) != 0 {
		t.Errorf("merged branches belong to merged-branch, got %+v", out)
	}
	// Without squash detection the squash and rebase merges are ordinary
	// stale branches again, exactly as merged-branch would see them.
	f.cfg.Detectors.MergedBranch.Mode = config.MergeAncestor
	var refs []string
	for _, x := range f.mustDetect() {
		refs = append(refs, x.Ref)
	}
	if !reflect.DeepEqual(refs, []string{"merged/rebase", "merged/squash"}) {
		t.Errorf("ancestor mode refs = %v", refs)
	}
}

func TestProtectedAndBaseBranchesAreSkipped(t *testing.T) {
	f := newFixture(t, true)
	f.branch("release/1.0", f.daysAgo(200))
	f.branch("develop", f.daysAgo(200))
	f.branch("master", f.daysAgo(200))
	f.branch("feat/normal", f.daysAgo(200))
	got := f.mustDetect()
	if len(got) != 1 || got[0].Ref != "feat/normal" {
		t.Fatalf("only the unprotected branch may be reported, got %+v", got)
	}
	// Base branches stay skipped even when nothing is protected.
	f.cfg.Git.ProtectedBranches = nil
	got = f.mustDetect()
	if len(got) != 2 || got[0].Ref != "feat/normal" || got[1].Ref != "release/1.0" {
		t.Errorf("base branches must be skipped without protection, got %+v", got)
	}
}

func TestCheckedOutInLinkedWorktreeIsBlocked(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/wt", f.daysAgo(100))
	wt := f.repo.AddWorktree("wt", "feat/wt")
	f.extraAllowed = []string{wt}
	for _, force := range []bool{false, true} {
		f.force = force
		got := f.only()
		if !got.HasRisk(findings.RiskCurrentBranch) || got.SuggestedAction.Type != findings.ActionNone {
			t.Errorf("force=%v: %+v", force, got)
		}
		if got.Confidence != findings.ConfidenceLow {
			t.Errorf("confidence %s", got.Confidence)
		}
	}
}

func TestIdenticalIDFromEveryWorktree(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/x", f.daysAgo(100))
	wt := f.repo.AddWorktree("wt", "")
	f.extraAllowed = []string{wt}
	fromMain, err := f.detect(f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	fromWT, err := f.detect(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromMain) != 1 || len(fromWT) != 1 {
		t.Fatalf("main %d worktree %d findings", len(fromMain), len(fromWT))
	}
	if fromMain[0].ID != fromWT[0].ID || fromWT[0].Path != f.repo.Dir {
		t.Errorf("ids %q vs %q, worktree path %q", fromMain[0].ID, fromWT[0].ID, fromWT[0].Path)
	}
}

func TestForcedOverride(t *testing.T) {
	ghOK := func(_ context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
		return []byte(`[{"headRefName":"feat/both"},{"headRefName":"feat/pr"}]`), nil
	}
	tests := []struct {
		name   string
		branch string
		pushed bool
		force  bool
		action findings.ActionType
		reason string
	}{
		{"unpushed forced", "feat/local", false, true, findings.ActionDeleteBranch, "forced: unpushed_commits"},
		{"open pr forced", "feat/pr", true, true, findings.ActionDeleteBranch, "forced: has_open_pr"},
		{"both forced", "feat/both", false, true, findings.ActionDeleteBranch, "forced: unpushed_commits, has_open_pr"},
		{"not forced", "feat/local", false, false, findings.ActionNone, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.cfg.Git.UseGH, f.gh, f.force = true, ghOK, tt.force
			if tt.pushed {
				f.pushed(tt.branch, f.daysAgo(100))
			} else {
				f.branch(tt.branch, f.daysAgo(100))
			}
			got := f.only()
			sa := got.SuggestedAction
			if sa.Type != tt.action || !got.Blocked() || got.Confidence != findings.ConfidenceLow {
				t.Fatalf("action %+v blocked=%v conf=%s", sa, got.Blocked(), got.Confidence)
			}
			if tt.action == findings.ActionNone {
				return
			}
			if sa.Args["verified"] != "forced" || sa.Command != "git branch -D "+tt.branch || sa.Reason != tt.reason {
				t.Errorf("forced action %+v", sa)
			}
		})
	}
}

func TestForceNeverOverridesCurrentBranchWithUnpushed(t *testing.T) {
	f := newFixture(t, true)
	f.branch("feat/wt", f.daysAgo(100))
	wt := f.repo.AddWorktree("wt", "feat/wt")
	f.extraAllowed, f.force = []string{wt}, true
	got := f.only()
	if got.SuggestedAction.Type != findings.ActionNone || !got.HasRisk(findings.RiskUnpushedCommits) || !got.HasRisk(findings.RiskCurrentBranch) {
		t.Errorf("%+v", got)
	}
}

func TestEmissionOrderIsByName(t *testing.T) {
	f := newFixture(t, true)
	for _, n := range []string{"feat/c", "feat/a", "feat/b"} {
		f.pushed(n, f.daysAgo(100))
	}
	var refs []string
	for _, x := range f.mustDetect() {
		refs = append(refs, x.Ref)
	}
	if !reflect.DeepEqual(refs, []string{"feat/a", "feat/b", "feat/c"}) {
		t.Errorf("order %v", refs)
	}
}

func TestTargetAndGuardRefusals(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/x", f.daysAgo(100))
	guard, err := scope.NewGuard(f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	env := &detect.Env{Config: f.cfg, Git: f.runner, Guard: guard, Now: f.now}
	tgt := scope.Target{Kind: scope.TargetProject, Path: f.repo.Dir}
	n := 0
	if err := New().Detect(context.Background(), env, tgt, func(findings.Finding) { n++ }); err != nil || n != 0 {
		t.Errorf("project targets are ignored: err=%v findings=%d", err, n)
	}

	other := testutil.ResolvedTempDir(t)
	outside, err := scope.NewGuard(other)
	if err != nil {
		t.Fatal(err)
	}
	env.Guard = outside
	if _, err := f.detectWith(env, f.repo.Dir); !errors.Is(err, scope.ErrOutsideScope) {
		t.Errorf("main worktree outside the guard must be refused, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.Guard = guard
	if err := New().Detect(ctx, env, scope.Target{Kind: scope.TargetRepo, Path: f.repo.Dir}, func(findings.Finding) {}); err == nil {
		t.Error("cancelled context must stop the scan")
	}
}

func TestDetectorNeverWrites(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/x", f.daysAgo(100))
	f.branch("feat/local", f.daysAgo(100))
	before := f.repo.Git("for-each-ref")
	statBefore, err := os.Stat(filepath.Join(f.repo.Dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	f.mustDetect()
	if after := f.repo.Git("for-each-ref"); after != before {
		t.Errorf("refs changed:\n%s\n%s", before, after)
	}
	statAfter, err := os.Stat(filepath.Join(f.repo.Dir, ".git", "index"))
	if err != nil || !statAfter.ModTime().Equal(statBefore.ModTime()) {
		t.Errorf("index touched: %v", err)
	}
}

func TestRegistration(t *testing.T) {
	d, ok := detect.Get(Name)
	if !ok || d.Category() != detect.CategoryGit || d.Name() != "stale-branch" || d.Description() == "" {
		t.Errorf("registration: %v %v", d, ok)
	}
}
