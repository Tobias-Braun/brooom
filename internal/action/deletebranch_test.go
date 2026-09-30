package action

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// branchFixture is a repository with a bare origin, a real guard and the
// real git runner. gh is faked so tests never run the real binary.
type branchFixture struct {
	t    *testing.T
	repo *testutil.Repo
	env  *Env
	act  deleteBranch
}

func newBranchFixture(t *testing.T) *branchFixture {
	t.Helper()
	git, err := gitx.NewExecRunner()
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	home := testutil.ResolvedTempDir(t)
	t.Setenv(config.HomeEnv, home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Keep the runner independent of the developer's git configuration.
	t.Setenv("GIT_CONFIG_GLOBAL", home+"/none")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := testutil.NewRepoWithRemote(t)
	guard, err := scope.NewGuard(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Git.ProtectedBranches = append(cfg.Git.ProtectedBranches, "keep/*")
	setGH(t, func(context.Context, string, []string, ...string) ([]byte, error) {
		return nil, errors.New("gh unavailable")
	})
	return &branchFixture{t: t, repo: repo, env: &Env{Config: cfg, Git: git, Guard: guard}}
}

func setGH(t *testing.T, fn gitx.GHRunner) {
	t.Helper()
	old := ghRunner
	ghRunner = fn
	t.Cleanup(func() { ghRunner = old })
}

// finding builds a delete-branch finding for the branch's current tip.
func (fx *branchFixture) finding(name, detector, verified string) findings.Finding {
	fx.t.Helper()
	tip := fx.repo.Git("rev-parse", "refs/heads/"+name)
	return fx.findingAt(name, detector, verified, tip)
}

func (fx *branchFixture) findingAt(name, detector, verified, tip string) findings.Finding {
	a := findings.SuggestedAction{Type: findings.ActionDeleteBranch}
	if verified != "" {
		a.Args = map[string]string{"verified": verified}
	}
	meta := map[string]string{}
	if tip != "" {
		meta["tip"] = tip
	}
	return findings.Finding{
		ID: "id-" + name, Detector: detector,
		Scope: findings.Scope{Type: findings.ScopeRepo, Path: fx.repo.Dir},
		Path:  fx.repo.Dir, Kind: findings.KindBranch, Ref: name,
		SuggestedAction: a, Meta: meta,
	}
}

func (fx *branchFixture) plan(f findings.Finding) (Step, error) {
	return fx.act.Plan(context.Background(), fx.env, f)
}

func (fx *branchFixture) branchExists(name string) bool {
	return fx.repo.Git("branch", "--list", name) != ""
}

// featureBranch creates name with one commit, returns to main.
func (fx *branchFixture) featureBranch(name string) {
	fx.t.Helper()
	fx.repo.Checkout("main")
	fx.repo.Git("checkout", "-q", "-b", name)
	fx.repo.Commit(strings.ReplaceAll(name, "/", "_")+".txt", name, "work on "+name, testutil.BaseTime.Add(time.Hour))
	fx.repo.Checkout("main")
}

// mustApply plans and applies, failing on any error.
func (fx *branchFixture) mustApply(f findings.Finding) (Step, session.Entry) {
	fx.t.Helper()
	step, err := fx.plan(f)
	if err != nil {
		fx.t.Fatalf("Plan: %v", err)
	}
	en, err := fx.act.Apply(context.Background(), fx.env, step)
	if err != nil {
		fx.t.Fatalf("Apply: %v", err)
	}
	return step, en
}

func wantBranchSkip(t *testing.T, err error, contains string) {
	t.Helper()
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("err = %v, want ErrSkipped", err)
	}
	if r := skipReason(err); !strings.Contains(r, contains) {
		t.Fatalf("skip reason = %q, want it to contain %q", r, contains)
	}
}

func TestDeleteBranchRegistered(t *testing.T) {
	a, ok := Get(findings.ActionDeleteBranch)
	if !ok || a.Type() != findings.ActionDeleteBranch {
		t.Fatalf("delete-branch not registered: %v %v", a, ok)
	}
}

func TestDeleteBranchMergedUsesDashD(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
	fx.repo.Push("main")
	f := fx.finding("feat/x", "merged-branch", "")

	step, en := fx.mustApply(f)
	if step.Command != "git branch -d -- feat/x" || !strings.Contains(step.Description, "-d") {
		t.Fatalf("step = %+v", step)
	}
	if fx.branchExists("feat/x") {
		t.Fatal("branch still exists")
	}
	if en.Status != session.StatusApplied || !en.Restorable || en.SizeBytes != 0 || en.Path != fx.repo.Dir || en.Ref != "feat/x" {
		t.Fatalf("entry = %+v", en)
	}
	if en.Undo["branch"] != "feat/x" || en.Undo["sha"] != f.Meta["tip"] {
		t.Fatalf("undo = %v", en.Undo)
	}
	// The plan names the reference that justified -d.
	if !strings.Contains(step.Description, "fully merged into HEAD") {
		t.Errorf("description = %q, want it to name HEAD", step.Description)
	}
	checkReachableHint(t, en.RecoveryHint, "git branch feat/x "+f.Meta["tip"], "main")
}

// checkReachableHint asserts the hint of a branch whose commits another ref
// still holds: it names that ref and does not warn about unreachable objects.
func checkReachableHint(t *testing.T, hint, cmd, ref string) {
	t.Helper()
	for _, want := range []string{cmd, "still reachable from " + ref} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q lacks %q", hint, want)
		}
	}
	for _, bad := range []string{"unreachable", "2 weeks", "reflog"} {
		if strings.Contains(hint, bad) {
			t.Errorf("hint %q must not mention %q for a branch that stays reachable", hint, bad)
		}
	}
}

// checkRecoveryHint asserts the hint of an unmerged, force-deleted branch is
// accurate about git: it names the command and the reflog deletion, and
// never promises a 90 day window.
func checkRecoveryHint(t *testing.T, hint, cmd string) {
	t.Helper()
	for _, want := range []string{cmd, "reflog", "unreachable", "2 weeks"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q lacks %q", hint, want)
		}
	}
	if strings.Contains(hint, "90") {
		t.Errorf("hint must not promise a 90 day window: %q", hint)
	}
}

func TestDeleteBranchMergedWhileOtherBranchCheckedOut(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.repo.Git("branch", "work")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
	fx.repo.Push("main")
	fx.repo.Checkout("work")
	f := fx.finding("feat/x", "merged-branch", "")

	step, _ := fx.mustApply(f)
	// git -d would refuse (HEAD is work) but the base ancestry is re-verified.
	if step.Command != "git branch -D -- feat/x" || !strings.Contains(step.Description, "merged into origin/main") {
		t.Fatalf("step = %+v", step)
	}
	if fx.branchExists("feat/x") {
		t.Fatal("branch still exists")
	}
}

func TestDeleteBranchSquash(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/sq")
	// The branch commits sit on a remote: the heuristic alone would not do.
	fx.repo.Git("push", "-q", "origin", "feat/sq")
	fx.repo.SquashMerge("feat/sq", "squash", testutil.BaseTime.Add(2*time.Hour))
	fx.repo.Push("main")
	f := fx.finding("feat/sq", "merged-branch", "squash")

	step, _ := fx.mustApply(f)
	if step.Command != "git branch -D -- feat/sq" || !strings.Contains(step.Description, "re-verified") {
		t.Fatalf("step = %+v", step)
	}
	if fx.branchExists("feat/sq") {
		t.Fatal("branch still exists")
	}
}

func TestDeleteBranchSquashNewCommitSkipped(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/sq")
	fx.repo.SquashMerge("feat/sq", "squash", testutil.BaseTime.Add(2*time.Hour))
	fx.repo.Push("main")
	f := fx.finding("feat/sq", "merged-branch", "squash")
	fx.repo.Checkout("feat/sq")
	fx.repo.Commit("late.txt", "late", "late work", testutil.BaseTime.Add(3*time.Hour))
	fx.repo.Checkout("main")

	_, err := fx.plan(f)
	wantBranchSkip(t, err, "branch has new commits since the scan")
	fx.env.Force = true
	_, err = fx.plan(f)
	wantBranchSkip(t, err, "branch has new commits since the scan")
}

func TestDeleteBranchSquashClaimNoLongerHolds(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/unsq")
	// The finding claims a squash merge that never happened.
	f := fx.finding("feat/unsq", "merged-branch", "squash")
	_, err := fx.plan(f)
	wantBranchSkip(t, err, "not fully merged; re-run with --force to delete with -D")
}

func TestDeleteBranchInRemote(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/r")
	fx.repo.Git("push", "-q", "origin", "feat/r") // no upstream: git -d cannot verify
	fx.repo.Fetch()
	f := fx.finding("feat/r", "stale-branch", "in-remote")

	step, _ := fx.mustApply(f)
	if step.Command != "git branch -D -- feat/r" || !strings.Contains(step.Description, "remote-tracking") {
		t.Fatalf("step = %+v", step)
	}
	if fx.branchExists("feat/r") {
		t.Fatal("branch still exists")
	}
}

func TestDeleteBranchInRemoteWithUpstreamUsesDashD(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/u")
	fx.repo.Push("feat/u")
	step, _ := fx.mustApply(fx.finding("feat/u", "stale-branch", "in-remote"))
	if step.Command != "git branch -d -- feat/u" {
		t.Fatalf("command = %q", step.Command)
	}
}

func TestDeleteBranchInRemoteNewLocalCommitSkipped(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/r")
	fx.repo.Git("push", "-q", "origin", "feat/r")
	fx.repo.Fetch()
	f := fx.finding("feat/r", "stale-branch", "in-remote")
	fx.repo.Checkout("feat/r")
	fx.repo.Commit("unpushed.txt", "u", "unpushed", testutil.BaseTime.Add(4*time.Hour))
	fx.repo.Checkout("main")

	_, err := fx.plan(f)
	wantBranchSkip(t, err, "new commits")
}

func TestDeleteBranchInRemoteRemoteRefGone(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/r")
	fx.repo.Git("push", "-q", "origin", "feat/r")
	fx.repo.Fetch()
	f := fx.finding("feat/r", "stale-branch", "in-remote")
	fx.repo.DeleteRemoteBranch("feat/r")
	fx.repo.Fetch()

	_, err := fx.plan(f)
	wantBranchSkip(t, err, "not fully merged")
	// --force skips the unpushed check and falls through to the -D rule.
	fx.env.Force = true
	step, err := fx.plan(f)
	if err != nil || step.Command != "git branch -D -- feat/r" || !strings.Contains(step.Description, "forced") {
		t.Fatalf("forced step = %+v, %v", step, err)
	}
}

func TestDeleteBranchUnmergedNeedsForce(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/wip")
	f := fx.finding("feat/wip", "merged-branch", "")

	_, err := fx.plan(f)
	wantBranchSkip(t, err, "not fully merged; re-run with --force to delete with -D")

	fx.env.Force = true
	step, en := fx.mustApply(f)
	if step.Command != "git branch -D -- feat/wip" || !strings.Contains(step.Description, "forced") {
		t.Fatalf("step = %+v", step)
	}
	if fx.branchExists("feat/wip") || en.Status != session.StatusApplied {
		t.Fatalf("branch exists or entry = %+v", en)
	}
	// Nothing else holds the commits, so this is the case that warrants the
	// unreachable-objects warning.
	checkRecoveryHint(t, en.RecoveryHint, "git branch feat/wip "+f.Meta["tip"])
}

func TestDeleteBranchRefusalsEvenWithForce(t *testing.T) {
	fx := newBranchFixture(t)
	fx.env.Force = true
	fx.env.Config.Git.BaseBranches = append(fx.env.Config.Git.BaseBranches, "basey")
	fx.featureBranch("basey")
	fx.featureBranch("keep/x")
	fx.featureBranch("current")
	fx.featureBranch("other")
	fx.repo.Checkout("current")
	tests := []struct{ name, branch, want string }{
		{"protected", "keep/x", "protected"},
		{"base", "basey", "base branch"},
		{"checked out", "current", "checked out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fx.plan(fx.finding(tc.branch, "merged-branch", ""))
			wantBranchSkip(t, err, tc.want)
		})
	}
}

func TestDeleteBranchLinkedWorktreeCheckedOut(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/wt")
	fx.repo.AddWorktree("wt", "feat/wt")
	_, err := fx.plan(fx.finding("feat/wt", "merged-branch", ""))
	wantBranchSkip(t, err, "checked out")
}

func TestDeleteBranchGone(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/gone")
	f := fx.finding("feat/gone", "merged-branch", "")
	fx.repo.Git("branch", "-D", "feat/gone")
	_, err := fx.plan(f)
	wantBranchSkip(t, err, "no longer exists")
}

func TestDeleteBranchTipMeta(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	_, err := fx.plan(fx.findingAt("feat/x", "merged-branch", "", ""))
	wantBranchSkip(t, err, "finding has no tip; re-run the scan")
	_, err = fx.plan(fx.findingAt("feat/x", "merged-branch", "", strings.Repeat("a", 40)))
	wantBranchSkip(t, err, "branch has new commits since the scan")
}

func TestDeleteBranchOpenPR(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/pr")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/pr")
	fx.repo.Push("main")
	f := fx.finding("feat/pr", "merged-branch", "")

	tests := []struct {
		name    string
		gh      gitx.GHRunner
		force   bool
		useGH   bool
		wantErr string
	}{
		{"open pr skips", func(context.Context, string, []string, ...string) ([]byte, error) {
			return []byte(`[{"headRefName":"feat/pr"}]`), nil
		}, false, true, "open pull request"},
		{"force overrides", func(context.Context, string, []string, ...string) ([]byte, error) {
			return []byte(`[{"headRefName":"feat/pr"}]`), nil
		}, true, true, ""},
		{"other pr does not block", func(context.Context, string, []string, ...string) ([]byte, error) {
			return []byte(`[{"headRefName":"feat/else"}]`), nil
		}, false, true, ""},
		{"unknown never blocks", func(context.Context, string, []string, ...string) ([]byte, error) {
			return nil, errors.New("offline")
		}, false, true, ""},
		{"use_gh off ignores gh", func(context.Context, string, []string, ...string) ([]byte, error) {
			return []byte(`[{"headRefName":"feat/pr"}]`), nil
		}, false, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setGH(t, tc.gh)
			fx.env.Force = tc.force
			fx.env.Config.Git.UseGH = tc.useGH
			_, err := fx.plan(f)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Plan: %v", err)
				}
				return
			}
			wantBranchSkip(t, err, tc.wantErr)
		})
	}
}

func TestDeleteBranchOutsideGuardRefused(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	f := fx.finding("feat/x", "merged-branch", "")
	other := testutil.ResolvedTempDir(t)
	g, err := scope.NewGuard(other)
	if err != nil {
		t.Fatal(err)
	}
	fx.env.Guard = g
	_, err = fx.plan(f)
	wantBranchSkip(t, err, "outside allowed roots")
	if !fx.branchExists("feat/x") {
		t.Fatal("branch must be untouched")
	}
}

func TestDeleteBranchNotARepository(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	f := fx.finding("feat/x", "merged-branch", "")
	f.Path = testutil.ResolvedTempDir(t)
	g, _ := scope.NewGuard(f.Path)
	fx.env.Guard = g
	_, err := fx.plan(f)
	wantBranchSkip(t, err, "not a git repository")
}

func TestDeleteBranchWrongKind(t *testing.T) {
	fx := newBranchFixture(t)
	f := fx.findingAt("x", "merged-branch", "", "abc")
	f.Kind = findings.KindDir
	_, err := fx.plan(f)
	wantBranchSkip(t, err, "not a branch finding")
	f.Kind, f.Ref = findings.KindBranch, ""
	_, err = fx.plan(f)
	wantBranchSkip(t, err, "not a branch finding")
}

func TestDeleteBranchHostileNames(t *testing.T) {
	fx := newBranchFixture(t)
	fx.env.Force = true
	fx.featureBranch("feat/x")
	tip := fx.repo.Git("rev-parse", "feat/x")
	for _, name := range []string{"-x", "--all", "-D", "a..b", "a b", "refs/heads/feat/x", "@{-1}", "@", "HEAD", "feat/x\nmain", "a~1", "a:b", "x.lock", ""} {
		t.Run(name, func(t *testing.T) {
			_, err := fx.plan(fx.findingAt(name, "merged-branch", "", tip))
			if !errors.Is(err, ErrSkipped) {
				t.Fatalf("Plan(%q) = %v, want a skip", name, err)
			}
			if !fx.branchExists("feat/x") || !fx.branchExists("main") {
				t.Fatal("hostile name must not delete anything")
			}
		})
	}
}

func TestDeleteBranchApplyReevaluates(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
	fx.repo.Push("main")
	f := fx.finding("feat/x", "merged-branch", "")
	step, err := fx.plan(f)
	if err != nil {
		t.Fatal(err)
	}
	// New commit lands between Plan and Apply.
	fx.repo.Checkout("feat/x")
	fx.repo.Commit("more.txt", "m", "more", testutil.BaseTime.Add(5*time.Hour))
	fx.repo.Checkout("main")

	en, err := fx.act.Apply(context.Background(), fx.env, step)
	if err != nil || en.Status != session.StatusSkipped || !strings.Contains(en.Error, "new commits") {
		t.Fatalf("entry = %+v, err = %v", en, err)
	}
	if !fx.branchExists("feat/x") {
		t.Fatal("branch must survive")
	}
}

func TestDeleteBranchApplyGitFailureIsFailedEntry(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
	fx.repo.Push("main")
	step, err := fx.plan(fx.finding("feat/x", "merged-branch", ""))
	if err != nil {
		t.Fatal(err)
	}
	// A stale lock on the ref makes git refuse the deletion.
	testutil.WriteFile(t, fx.repo.Dir, ".git/refs/heads/feat/x.lock", "")
	en, err := fx.act.Apply(context.Background(), fx.env, step)
	if err == nil || en.Status != session.StatusFailed || en.Error == "" {
		t.Fatalf("entry = %+v, err = %v", en, err)
	}
}

func TestDeleteBranchUndo(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
	fx.repo.Push("main")
	f := fx.finding("feat/x", "merged-branch", "")
	_, en := fx.mustApply(f)
	ctx := context.Background()

	if err := fx.act.Undo(ctx, fx.env, en); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := fx.repo.Git("rev-parse", "refs/heads/feat/x"); got != f.Meta["tip"] {
		t.Fatalf("restored at %s, want %s", got, f.Meta["tip"])
	}
	if err := fx.act.Undo(ctx, fx.env, en); err != nil {
		t.Fatalf("second Undo must be idempotent: %v", err)
	}

	// Name taken by a branch elsewhere.
	fx.repo.Git("branch", "-D", "feat/x")
	fx.repo.Git("branch", "feat/x", "main")
	err := fx.act.Undo(ctx, fx.env, en)
	if err == nil || !strings.Contains(err.Error(), "branch feat/x already exists at "+fx.repo.Head()) {
		t.Fatalf("Undo = %v", err)
	}
}

func TestDeleteBranchUndoCommitGarbageCollected(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.env.Force = true
	f := fx.finding("feat/x", "merged-branch", "")
	_, en := fx.mustApply(f)
	fx.repo.Git("reflog", "expire", "--expire=now", "--all")
	fx.repo.Git("gc", "-q", "--prune=now")

	err := fx.act.Undo(context.Background(), fx.env, en)
	want := "commit " + f.Meta["tip"] + " no longer exists (garbage-collected); cannot restore"
	if err == nil || err.Error() != want {
		t.Fatalf("Undo = %v, want %q", err, want)
	}
}

func TestDeleteBranchUndoRefusals(t *testing.T) {
	fx := newBranchFixture(t)
	sha := fx.repo.Head()
	other, _ := scope.NewGuard(testutil.ResolvedTempDir(t))
	tests := []struct {
		name  string
		entry session.Entry
		guard *scope.Guard
	}{
		{"option name", session.Entry{Path: fx.repo.Dir, Undo: map[string]string{"branch": "-f", "sha": sha}}, nil},
		{"bad sha", session.Entry{Path: fx.repo.Dir, Undo: map[string]string{"branch": "x", "sha": "--force"}}, nil},
		{"missing undo", session.Entry{Path: fx.repo.Dir}, nil},
		{"outside guard", session.Entry{Path: fx.repo.Dir, Undo: map[string]string{"branch": "x", "sha": sha}}, other},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := *fx.env
			if tc.guard != nil {
				env.Guard = tc.guard
			}
			if err := fx.act.Undo(context.Background(), &env, tc.entry); err == nil {
				t.Fatal("Undo succeeded, want an error")
			}
			if fx.branchExists("x") {
				t.Fatal("no branch may be created")
			}
		})
	}
}

// TestDeleteBranchRunEscalation drives the fallback of run directly: git
// refuses -d, and -D follows only when the merge is verified or forced.
func TestDeleteBranchRunEscalation(t *testing.T) {
	tests := []struct {
		name     string
		merged   bool
		force    bool
		wantGone bool
	}{
		{"verified merged escalates", true, false, true},
		{"unverified is skipped", false, false, false},
		{"unverified with force escalates", false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newBranchFixture(t)
			fx.featureBranch("feat/x")
			fx.repo.Git("branch", "work")
			if tc.merged {
				fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
				fx.repo.Push("main")
			}
			fx.repo.Checkout("work")
			// Evaluate with force to obtain a decision for any case, then
			// run with the case's own force setting and the plain -d flag.
			fx.env.Force = true
			d, err := evaluate(context.Background(), fx.env, fx.finding("feat/x", "merged-branch", ""))
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			fx.env.Force = tc.force
			d.flag = flagSafe
			_, err = d.run(context.Background(), fx.env)
			if tc.wantGone == fx.branchExists("feat/x") {
				t.Fatalf("branch exists = %v, run err = %v", !tc.wantGone, err)
			}
			if !tc.wantGone {
				wantBranchSkip(t, err, "--force")
			}
		})
	}
}
