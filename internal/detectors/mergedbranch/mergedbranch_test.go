package mergedbranch_test

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/detectors/mergedbranch"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// at returns BaseTime shifted by n hours so commits get distinct dates.
func at(n int) time.Time { return testutil.BaseTime.Add(time.Duration(n) * time.Hour) }

// fixture bundles a repository with a bare origin, a real guard and an
// environment whose clock is fixed 60 days after the base commit.
type fixture struct {
	t    *testing.T
	repo *testutil.Repo
	cfg  *config.Config
	env  *detect.Env
	det  *mergedbranch.Detector
	gh   gitx.GHRunner
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := testutil.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BROOOM_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", home+"/.gitconfig-none")
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	repo := testutil.NewRepoWithRemote(t)
	guard, err := scope.NewGuard(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	f := &fixture{t: t, repo: repo, cfg: cfg, det: mergedbranch.New()}
	f.env = &detect.Env{Config: cfg, Git: runner, Guard: guard, Now: testutil.BaseTime.Add(60 * 24 * time.Hour)}
	// No gh by default: the lookup is reported unknown without spawning it.
	f.setGH(func(context.Context, string, []string, ...string) ([]byte, error) {
		return nil, errors.New("gh unavailable")
	})
	return f
}

func (f *fixture) setGH(gh gitx.GHRunner) {
	f.gh = gh
	f.det = &mergedbranch.Detector{GH: gh}
}

// feature creates a branch with one commit per file off main and returns to main.
func (f *fixture) feature(name string, files ...string) {
	f.t.Helper()
	f.repo.Git("checkout", "-q", "-b", name, "main")
	for _, file := range files {
		f.n++
		f.repo.Commit(file, name+file, "work on "+file, at(f.n))
	}
	f.repo.Checkout("main")
}

// merge merges a branch into main with a merge commit. A merge commit keeps
// the branch tip different from the base tip, so the branch is not mistaken
// for an unstarted one.
func (f *fixture) merge(name string) {
	f.t.Helper()
	f.n++
	f.repo.GitAt(at(f.n), "merge", "-q", "--no-ff", "-m", "merge "+name, name)
}

// publish pushes main and refreshes origin/main, the base ref of the detector.
func (f *fixture) publish() {
	f.t.Helper()
	f.repo.Push("main")
	f.repo.Fetch()
}

func (f *fixture) run(target scope.Target) ([]findings.Finding, error) {
	f.t.Helper()
	var out []findings.Finding
	err := f.det.Detect(context.Background(), f.env, target, func(x findings.Finding) { out = append(out, x) })
	return out, err
}

func (f *fixture) target(dir string) scope.Target {
	return scope.Target{
		Kind:  scope.TargetRepo,
		Path:  dir,
		Scope: findings.Scope{Type: findings.ScopeRepo, Path: f.repo.Dir},
	}
}

func (f *fixture) detect() []findings.Finding {
	f.t.Helper()
	out, err := f.run(f.target(f.repo.Dir))
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func byRef(fs []findings.Finding, ref string) *findings.Finding {
	for i := range fs {
		if fs[i].Ref == ref {
			return &fs[i]
		}
	}
	return nil
}

func refs(fs []findings.Finding) []string {
	var out []string
	for _, x := range fs {
		out = append(out, x.Ref)
	}
	return out
}

func mustFind(t *testing.T, fs []findings.Finding, ref string) findings.Finding {
	t.Helper()
	x := byRef(fs, ref)
	if x == nil {
		t.Fatalf("no finding for %q in %v", ref, refs(fs))
	}
	return *x
}

func evidenceCodes(x findings.Finding) []string {
	var out []string
	for _, e := range x.Evidence {
		out = append(out, e.Code)
	}
	return out
}

func TestRegistration(t *testing.T) {
	d, ok := detect.Get("merged-branch")
	if !ok {
		t.Fatal("merged-branch is not registered")
	}
	if d.Category() != detect.CategoryGit || d.Name() != "merged-branch" || d.Description() == "" {
		t.Errorf("unexpected metadata: %s %s %q", d.Name(), d.Category(), d.Description())
	}
}

func TestMergeMethods(t *testing.T) {
	tests := []mergeCase{
		{
			name:       "fast-forward",
			merge:      func(f *fixture) { f.repo.Git("merge", "-q", "--ff-only", "feat/x") },
			mode:       config.MergeAncestorSquash,
			wantMethod: "ancestor",
			wantCmd:    "git branch -d feat/x",
			wantReason: "fully merged into origin/main",
			wantEv:     "merged_into",
		},
		{
			name:       "merge commit",
			merge:      func(f *fixture) { f.repo.GitAt(at(50), "merge", "-q", "--no-ff", "-m", "merge", "feat/x") },
			mode:       config.MergeAncestorSquash,
			wantMethod: "ancestor",
			wantCmd:    "git branch -d feat/x",
			wantReason: "fully merged into origin/main",
			wantEv:     "merged_into",
		},
		{
			name:       "squash multi-commit",
			merge:      func(f *fixture) { f.repo.SquashMerge("feat/x", "squashed", at(50)) },
			mode:       config.MergeAncestorSquash,
			wantMethod: "squash",
			wantCmd:    "git branch -D feat/x",
			wantArgs:   map[string]string{"verified": "squash"},
			wantReason: "squash-merged into origin/main; -D is required because git cannot see the squash merge",
			wantEv:     "squash_merged_into",
		},
		{
			name:       "rebase",
			merge:      func(f *fixture) { f.repo.RebaseMerge("feat/x", at(50)) },
			mode:       config.MergeAncestorSquash,
			wantMethod: "rebase",
			wantCmd:    "git branch -D feat/x",
			wantArgs:   map[string]string{"verified": "squash"},
			wantReason: "rebase-merged into origin/main; -D is required because git cannot see the squash merge",
			wantEv:     "squash_merged_into",
		},
		{
			name:  "not merged",
			merge: func(f *fixture) {},
			mode:  config.MergeAncestorSquash,
		},
		{
			name:  "mode ancestor ignores a squash merge",
			merge: func(f *fixture) { f.repo.SquashMerge("feat/x", "squashed", at(50)) },
			mode:  config.MergeAncestor,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runMergeCase(t, tt) })
	}
}

// mergeCase describes one way of merging feat/x into main and the finding
// expected for it; an empty wantMethod means the branch is not reported.
type mergeCase struct {
	name       string
	merge      func(f *fixture)
	mode       config.MergeMode
	wantMethod string
	wantCmd    string
	wantArgs   map[string]string
	wantReason string
	wantEv     string
}

func runMergeCase(t *testing.T, tt mergeCase) {
	f := newFixture(t)
	f.cfg.Detectors.MergedBranch.Mode = tt.mode
	f.feature("feat/x", "a.txt", "b.txt")
	f.repo.Push("feat/x")
	tip := f.repo.Git("rev-parse", "feat/x")
	tt.merge(f)
	f.publish()

	got := f.detect()
	if tt.wantMethod == "" {
		if len(got) != 0 {
			t.Fatalf("want no findings, got %v", refs(got))
		}
		return
	}
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %v", refs(got))
	}
	x := got[0]
	checkIdentity(t, f, x)
	checkAction(t, x, tt)
	checkEvidenceAndMeta(t, f, x, tt, tip)
}

func checkIdentity(t *testing.T, f *fixture, x findings.Finding) {
	t.Helper()
	if x.Detector != "merged-branch" || x.Kind != findings.KindBranch || x.Ref != "feat/x" ||
		x.Path != f.repo.Dir || x.SizeBytes != 0 || x.Confidence != findings.ConfidenceHigh {
		t.Errorf("unexpected finding: %+v", x)
	}
	if x.ID != findings.NewID("merged-branch", findings.KindBranch, f.repo.Dir, "feat/x") {
		t.Errorf("unexpected id %s", x.ID)
	}
	if x.Scope.Type != findings.ScopeRepo || x.Scope.Path != f.repo.Dir {
		t.Errorf("scope = %+v", x.Scope)
	}
}

func checkAction(t *testing.T, x findings.Finding, tt mergeCase) {
	t.Helper()
	a := x.SuggestedAction
	if a.Type != findings.ActionDeleteBranch || a.Command != tt.wantCmd || a.Reason != tt.wantReason {
		t.Errorf("action = %+v", a)
	}
	if len(a.Args) != len(tt.wantArgs) || a.Args["verified"] != tt.wantArgs["verified"] {
		t.Errorf("args = %v, want %v", a.Args, tt.wantArgs)
	}
}

func checkEvidenceAndMeta(t *testing.T, f *fixture, x findings.Finding, tt mergeCase, tip string) {
	t.Helper()
	if len(x.RiskFlags) != 0 {
		t.Errorf("flags = %v, want none (pushed, old, not blocked)", x.RiskFlags)
	}
	wantCodes := []string{tt.wantEv, "last_commit_age", "open_pr_unknown"}
	if !slices.Equal(evidenceCodes(x), wantCodes) {
		t.Errorf("evidence = %v, want %v", evidenceCodes(x), wantCodes)
	}
	if x.Evidence[0].Value != "origin/main" {
		t.Errorf("evidence value = %v", x.Evidence[0].Value)
	}
	checkMeta(t, f, x, tt, tip)
}

func checkMeta(t *testing.T, f *fixture, x findings.Finding, tt mergeCase, tip string) {
	t.Helper()
	m := x.Meta
	if m["tip"] != tip || m["base"] != "origin/main" || m["merge_method"] != tt.wantMethod ||
		m["upstream"] != "origin/feat/x" || m["open_pr_check"] != "unknown" {
		t.Errorf("meta = %v", m)
	}
	if x.LastModified == nil || !x.LastModified.Equal(at(f.n)) || x.AgeDays != f.env.AgeDays(at(f.n)) {
		t.Errorf("last modified = %v, age = %d", x.LastModified, x.AgeDays)
	}
}

func TestSkippedBranches(t *testing.T) {
	f := newFixture(t)
	// A merged branch that is only reported because it has work.
	f.feature("feat/done", "a.txt")
	f.merge("feat/done")
	f.publish()
	// Unstarted: created at the current base tip and never pushed.
	f.repo.Branch("unstarted")
	// Base-named branches are skipped even when they are behind the base.
	f.repo.Git("branch", "develop", "HEAD~1")
	f.repo.Git("branch", "master", "HEAD~1")
	f.repo.Git("branch", "trunk")

	got := f.detect()
	if !slices.Equal(refs(got), []string{"feat/done"}) {
		t.Fatalf("got %v, want only feat/done", refs(got))
	}
}

func TestPushedBranchAtBaseTipIsReported(t *testing.T) {
	f := newFixture(t)
	f.repo.Branch("pushed")
	f.repo.Push("pushed")
	got := f.detect()
	x := mustFind(t, got, "pushed")
	if x.SuggestedAction.Type != findings.ActionDeleteBranch {
		t.Errorf("action = %+v", x.SuggestedAction)
	}
}

func TestProtectedBranch(t *testing.T) {
	for _, force := range []bool{false, true} {
		f := newFixture(t)
		f.env.Force = force
		f.feature("release/1.0", "r.txt")
		f.merge("release/1.0")
		f.publish()

		x := mustFind(t, f.detect(), "release/1.0")
		if !slices.Contains(x.RiskFlags, findings.RiskProtectedBranch) {
			t.Errorf("force=%v flags = %v", force, x.RiskFlags)
		}
		a := x.SuggestedAction
		if a.Type != findings.ActionNone || a.Command != "" || a.Args != nil {
			t.Errorf("force=%v action = %+v", force, a)
		}
		if !strings.Contains(a.Reason, "protected") || !strings.Contains(a.Reason, "--force does not override") {
			t.Errorf("force=%v reason = %q", force, a.Reason)
		}
	}
}

func TestProtectedFromRepoConfig(t *testing.T) {
	f := newFixture(t)
	f.feature("keep/me", "k.txt")
	f.merge("keep/me")
	f.publish()
	f.repo.WriteFile(".brooom.json", `{"protected_branches":["keep/*"]}`)

	x := mustFind(t, f.detect(), "keep/me")
	if !slices.Contains(x.RiskFlags, findings.RiskProtectedBranch) || x.SuggestedAction.Type != findings.ActionNone {
		t.Errorf("finding = %+v", x)
	}
}

func TestCheckedOutBranch(t *testing.T) {
	for _, force := range []bool{false, true} {
		f := newFixture(t)
		f.env.Force = force
		f.feature("feat/wt", "w.txt")
		f.merge("feat/wt")
		f.publish()
		wt := f.repo.AddWorktree("wt", "feat/wt")

		x := mustFind(t, f.detect(), "feat/wt")
		if !slices.Contains(x.RiskFlags, findings.RiskCurrentBranch) {
			t.Fatalf("force=%v flags = %v", force, x.RiskFlags)
		}
		if !namesWorktree(x, gitx.NormalizePath(wt)) {
			t.Errorf("evidence does not name %s: %+v", wt, x.Evidence)
		}
		a := x.SuggestedAction
		if a.Type != findings.ActionNone || a.Command != "" || !strings.Contains(a.Reason, "--force does not override") {
			t.Errorf("force=%v action = %+v", force, a)
		}
	}
}

// namesWorktree reports whether the current_branch_worktree evidence names path.
func namesWorktree(x findings.Finding, path string) bool {
	for _, e := range x.Evidence {
		if e.Code == "current_branch_worktree" && strings.Contains(e.Message, path) {
			return true
		}
	}
	return false
}

func TestCheckedOutInMainWorktree(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/here", "h.txt")
	f.merge("feat/here")
	f.publish()
	f.repo.Checkout("feat/here")

	x := mustFind(t, f.detect(), "feat/here")
	if !slices.Contains(x.RiskFlags, findings.RiskCurrentBranch) || x.SuggestedAction.Type != findings.ActionNone {
		t.Errorf("finding = %+v", x)
	}
}

func TestLinkedWorktreeTargetYieldsMainPathAndSameID(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/a", "a.txt")
	f.merge("feat/a")
	f.publish()
	wt := f.repo.AddWorktree("linked", "")

	fromMain := mustFind(t, f.detect(), "feat/a")
	got, err := f.run(f.target(wt))
	if err != nil {
		t.Fatal(err)
	}
	fromWT := mustFind(t, got, "feat/a")
	if fromWT.Path != f.repo.Dir {
		t.Errorf("path = %q, want main worktree %q", fromWT.Path, f.repo.Dir)
	}
	if fromWT.ID != fromMain.ID {
		t.Errorf("ids differ: %s vs %s", fromWT.ID, fromMain.ID)
	}
}

func TestOpenPR(t *testing.T) {
	prs := func(calls *atomic.Int32) gitx.GHRunner {
		return func(context.Context, string, []string, ...string) ([]byte, error) {
			calls.Add(1)
			return []byte(`[{"headRefName":"feat/pr"}]`), nil
		}
	}
	setup := func(t *testing.T) (*fixture, *atomic.Int32) {
		f := newFixture(t)
		var calls atomic.Int32
		f.setGH(prs(&calls))
		f.feature("feat/pr", "p.txt")
		f.feature("feat/nopr", "q.txt")
		f.merge("feat/pr")
		f.merge("feat/nopr")
		f.publish()
		return f, &calls
	}

	t.Run("blocked", func(t *testing.T) {
		f, _ := setup(t)
		got := f.detect()
		x := mustFind(t, got, "feat/pr")
		if !slices.Contains(x.RiskFlags, findings.RiskHasOpenPR) || x.SuggestedAction.Type != findings.ActionNone {
			t.Errorf("finding = %+v", x)
		}
		if x.SuggestedAction.Command != "" || x.Meta["open_pr_check"] != "ok" {
			t.Errorf("finding = %+v", x)
		}
		other := mustFind(t, got, "feat/nopr")
		if slices.Contains(other.RiskFlags, findings.RiskHasOpenPR) || other.SuggestedAction.Type != findings.ActionDeleteBranch {
			t.Errorf("other = %+v", other)
		}
		if slices.Contains(evidenceCodes(other), "open_pr_unknown") {
			t.Errorf("evidence = %v", evidenceCodes(other))
		}
	})

	t.Run("forced", func(t *testing.T) {
		f, _ := setup(t)
		f.env.Force = true
		x := mustFind(t, f.detect(), "feat/pr")
		a := x.SuggestedAction
		if a.Type != findings.ActionDeleteBranch || a.Command != "git branch -d feat/pr" {
			t.Errorf("action = %+v", a)
		}
		if !strings.HasPrefix(a.Reason, "forced:") || !strings.Contains(a.Reason, "has_open_pr") {
			t.Errorf("reason = %q", a.Reason)
		}
	})

	t.Run("open PR plus protected stays none under force", func(t *testing.T) {
		f := newFixture(t)
		f.env.Force = true
		f.setGH(func(context.Context, string, []string, ...string) ([]byte, error) {
			return []byte(`[{"headRefName":"release/2"}]`), nil
		})
		f.feature("release/2", "r.txt")
		f.merge("release/2")
		f.publish()
		x := mustFind(t, f.detect(), "release/2")
		if x.SuggestedAction.Type != findings.ActionNone {
			t.Errorf("action = %+v", x.SuggestedAction)
		}
		if !strings.Contains(x.SuggestedAction.Reason, "open pull request") {
			t.Errorf("reason should explain every flag: %q", x.SuggestedAction.Reason)
		}
	})

	t.Run("disabled makes no gh call", func(t *testing.T) {
		f, calls := setup(t)
		f.cfg.Git.UseGH = false
		x := mustFind(t, f.detect(), "feat/pr")
		if calls.Load() != 0 {
			t.Errorf("gh called %d times", calls.Load())
		}
		if x.Meta["open_pr_check"] != "disabled" || x.SuggestedAction.Type != findings.ActionDeleteBranch {
			t.Errorf("finding = %+v", x)
		}
	})
}

func TestUnknownPRIsNotBlocking(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/u", "u.txt")
	f.merge("feat/u")
	f.publish()
	x := mustFind(t, f.detect(), "feat/u")
	if x.Meta["open_pr_check"] != "unknown" || x.SuggestedAction.Type != findings.ActionDeleteBranch {
		t.Errorf("finding = %+v", x)
	}
	if !slices.Contains(evidenceCodes(x), "open_pr_unknown") {
		t.Errorf("evidence = %v", evidenceCodes(x))
	}
}

func TestUpstreamGoneAndNeverPushed(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/gone", "g.txt")
	f.repo.Push("feat/gone")
	f.feature("feat/local", "l.txt")
	f.merge("feat/gone")
	f.merge("feat/local")
	f.publish()
	f.repo.DeleteRemoteBranch("feat/gone")
	f.repo.Fetch()

	got := f.detect()
	gone := mustFind(t, got, "feat/gone")
	if !slices.Equal(gone.RiskFlags, []findings.RiskFlag{findings.RiskUpstreamGone}) {
		t.Errorf("gone flags = %v", gone.RiskFlags)
	}
	local := mustFind(t, got, "feat/local")
	if !slices.Equal(local.RiskFlags, []findings.RiskFlag{findings.RiskNeverPushed}) {
		t.Errorf("local flags = %v", local.RiskFlags)
	}
	if local.SuggestedAction.Type != findings.ActionDeleteBranch {
		t.Errorf("informational flags must not block: %+v", local.SuggestedAction)
	}
	if _, ok := local.Meta["upstream"]; ok {
		t.Errorf("upstream meta set without upstream: %v", local.Meta)
	}
}

func TestRecentlyModifiedIsInformationalAndAgeIsNotGated(t *testing.T) {
	f := newFixture(t)
	f.cfg.Thresholds.MinAgeDays = 30
	f.feature("feat/fresh", "f.txt")
	f.merge("feat/fresh")
	f.publish()
	f.env.Now = at(f.n).Add(time.Hour)

	x := mustFind(t, f.detect(), "feat/fresh")
	if !slices.Contains(x.RiskFlags, findings.RiskRecentlyModified) {
		t.Errorf("flags = %v", x.RiskFlags)
	}
	if x.SuggestedAction.Type != findings.ActionDeleteBranch {
		t.Errorf("action = %+v", x.SuggestedAction)
	}
}

func TestFlagOrderIsStable(t *testing.T) {
	f := newFixture(t)
	f.setGH(func(context.Context, string, []string, ...string) ([]byte, error) {
		return []byte(`[{"headRefName":"release/3"}]`), nil
	})
	f.feature("release/3", "r.txt")
	f.merge("release/3")
	f.publish()
	f.repo.AddWorktree("w", "release/3")
	f.env.Now = at(f.n).Add(time.Hour)

	x := mustFind(t, f.detect(), "release/3")
	want := []findings.RiskFlag{
		findings.RiskCurrentBranch, findings.RiskProtectedBranch, findings.RiskHasOpenPR,
		findings.RiskNeverPushed, findings.RiskRecentlyModified,
	}
	if !slices.Equal(x.RiskFlags, want) {
		t.Errorf("flags = %v, want %v", x.RiskFlags, want)
	}
}

func TestIncludeRemote(t *testing.T) {
	setup := func(t *testing.T, include bool) []findings.Finding {
		f := newFixture(t)
		f.cfg.Detectors.MergedBranch.IncludeRemote = include
		f.feature("feat/r", "r.txt")
		f.repo.Push("feat/r")
		f.feature("feat/open", "o.txt")
		f.repo.Push("feat/open")
		f.feature("release/9", "z.txt")
		f.repo.Push("release/9")
		f.merge("feat/r")
		f.merge("release/9")
		f.publish()
		return f.detect()
	}

	t.Run("off", func(t *testing.T) {
		got := setup(t, false)
		if byRef(got, "origin/feat/r") != nil {
			t.Errorf("remote reported: %v", refs(got))
		}
	})

	t.Run("on", func(t *testing.T) {
		got := setup(t, true)
		x := mustFind(t, got, "origin/feat/r")
		if x.SuggestedAction.Type != findings.ActionNone || x.SuggestedAction.Reason != "remote branch deletion is out of scope" ||
			x.Confidence != findings.ConfidenceHigh || x.Kind != findings.KindBranch {
			t.Errorf("finding = %+v", x)
		}
		if x.Meta["remote"] != "true" || x.Meta["tip"] == "" {
			t.Errorf("meta = %v", x.Meta)
		}
		for _, skipped := range []string{"origin/main", "origin/HEAD", "origin/release/9", "origin/feat/open"} {
			if byRef(got, skipped) != nil {
				t.Errorf("%s must not be reported: %v", skipped, refs(got))
			}
		}
		// The local twin is still reported separately with its own action.
		if mustFind(t, got, "feat/r").SuggestedAction.Type != findings.ActionDeleteBranch {
			t.Errorf("local branch lost its action")
		}
	})
}

func TestFindingsAreSortedByName(t *testing.T) {
	f := newFixture(t)
	for _, n := range []string{"zeta", "alpha", "mid"} {
		f.feature(n, n+".txt")
		f.merge(n)
	}
	f.publish()
	got := refs(f.detect())
	if !slices.Equal(got, []string{"alpha", "mid", "zeta"}) {
		t.Errorf("order = %v", got)
	}
}

func TestNoBase(t *testing.T) {
	t.Run("no candidate exists", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Git.BaseBranches = []string{"nonexistent"}
		f.repo.Git("remote", "set-head", "origin", "--delete")
		f.feature("feat/x", "x.txt")
		if got := f.detect(); len(got) != 0 {
			t.Errorf("got %v", refs(got))
		}
	})

	t.Run("unborn HEAD", func(t *testing.T) {
		f := newFixture(t)
		dir := testutil.ResolvedTempDir(t)
		if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
			t.Skipf("git init: %v %s", err, out)
		}
		guard, err := scope.NewGuard(dir)
		if err != nil {
			t.Fatal(err)
		}
		f.env.Guard = guard
		got, err := f.run(scope.Target{Kind: scope.TargetRepo, Path: dir, Scope: findings.Scope{Type: findings.ScopeRepo, Path: dir}})
		if err != nil || len(got) != 0 {
			t.Errorf("got %v, err %v", refs(got), err)
		}
	})
}

func TestTargetOutsideGuardIsAnError(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/x", "x.txt")
	f.merge("feat/x")
	f.publish()
	other := testutil.ResolvedTempDir(t)
	guard, err := scope.NewGuard(other)
	if err != nil {
		t.Fatal(err)
	}
	f.env.Guard = guard

	got, err := f.run(f.target(f.repo.Dir))
	if err == nil {
		t.Fatal("want an error")
	}
	if !errors.Is(err, scope.ErrOutsideScope) {
		t.Errorf("error does not wrap ErrOutsideScope: %v", err)
	}
	if !strings.Contains(err.Error(), f.repo.Dir) {
		t.Errorf("error does not name the paths: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("emitted findings with unresolved path: %v", refs(got))
	}
}

func TestNonRepoTargetsAreIgnored(t *testing.T) {
	f := newFixture(t)
	for _, kind := range []scope.TargetKind{scope.TargetProject, scope.TargetUser} {
		got, err := f.run(scope.Target{Kind: kind, Path: f.repo.Dir})
		if err != nil || len(got) != 0 {
			t.Errorf("%s: got %v, err %v", kind, got, err)
		}
	}
}

func TestCancelledContext(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/x", "x.txt")
	f.merge("feat/x")
	f.publish()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := f.det.Detect(ctx, f.env, f.target(f.repo.Dir), func(findings.Finding) {})
	if err == nil {
		t.Fatal("want a context error")
	}
}

func TestDetectorDoesNotModifyRepository(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/x", "x.txt")
	f.merge("feat/x")
	f.publish()
	before := f.repo.Git("for-each-ref")
	status := f.repo.Git("status", "--porcelain")
	f.detect()
	if after := f.repo.Git("for-each-ref"); after != before {
		t.Errorf("refs changed:\n%s\n%s", before, after)
	}
	if got := f.repo.Git("status", "--porcelain"); got != status {
		t.Errorf("status changed: %q", got)
	}
}

func TestQuotedCommandForOddBranchNames(t *testing.T) {
	f := newFixture(t)
	name := "feat/it's"
	f.feature(name, "q.txt")
	f.merge(name)
	f.publish()
	x := mustFind(t, f.detect(), name)
	if want := `git branch -d 'feat/it'\''s'`; x.SuggestedAction.Command != want {
		t.Errorf("command = %q, want %q", x.SuggestedAction.Command, want)
	}
}
