package stalebranch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// failingRunner fails the git invocations selected by fail.
type failingRunner struct {
	gitx.Runner
	fail func(args []string) bool
}

func (r failingRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if r.fail(args) {
		return "", errors.New("simulated git failure")
	}
	return r.Runner.Run(ctx, dir, args...)
}

func (f *fixture) detectFailing(fail func(args []string) bool) ([]findings.Finding, error) {
	f.t.Helper()
	guard, err := scope.NewGuard(f.repo.Dir)
	if err != nil {
		f.t.Fatal(err)
	}
	runner := failingRunner{Runner: f.runner, fail: fail}
	env := &detect.Env{Config: f.cfg, Git: runner, Repos: gitx.NewCache(runner), Guard: guard, Now: f.now}
	return f.detectWith(env, f.repo.Dir)
}

// evidenceByCode returns the evidence entry with the given code.
func evidenceByCode(t *testing.T, f findings.Finding, code string) findings.Evidence {
	t.Helper()
	for _, e := range f.Evidence {
		if e.Code == code {
			return e
		}
	}
	t.Fatalf("no %s evidence in %+v", code, f.Evidence)
	return findings.Evidence{}
}

// TestRemotelessCountsOnlyOwnCommits: in a repository without a remote a
// branch with one commit of its own used to be reported with the history
// shared with main ("2 commits exist on no remote"). The displayed count is
// the number of commits only this branch holds; the block stays.
func TestRemotelessCountsOnlyOwnCommits(t *testing.T) {
	f := newFixture(t, false)
	f.branch("feat/local", f.daysAgo(100))
	got := f.only()
	if !got.Blocked() || got.SuggestedAction.Type != findings.ActionNone {
		t.Fatalf("the unpushed block must remain: %+v", got)
	}
	wantReason := "1 commit exists only on this branch; deleting would lose them (re-run with --force to override)"
	if got.SuggestedAction.Reason != wantReason {
		t.Errorf("reason = %q, want %q", got.SuggestedAction.Reason, wantReason)
	}
	unique := evidenceByCode(t, got, "unique_commits")
	if unique.Value != 1 || unique.Message != "1 commit exists only on this branch" {
		t.Errorf("unique_commits = %+v", unique)
	}
	// The gate keeps counting every commit on no remote (base plus branch).
	if gate := evidenceByCode(t, got, "unpushed_commits"); gate.Value != 2 {
		t.Errorf("unpushed_commits = %+v", gate)
	}
}

// TestBranchSafeOnSiblingIsStillBlockedButNotCounted: all commits also exist
// on another local branch, so nothing would be lost; the gate still blocks
// because no remote has them, and the wording says exactly that.
func TestBranchSafeOnSiblingIsStillBlockedButNotCounted(t *testing.T) {
	f := newFixture(t, false)
	f.branch("feat/a", f.daysAgo(100))
	f.repo.Git("branch", "feat/b", "feat/a")
	got := f.mustDetect()
	if len(got) != 2 {
		t.Fatalf("want both branches, got %d", len(got))
	}
	r := got[0].SuggestedAction.Reason
	if !strings.Contains(r, "also exist on other local branches") || !strings.Contains(r, "no remote has them") || strings.Contains(r, "lose") {
		t.Errorf("reason = %q", r)
	}
}

// TestGitErrorsAreSurfaced: a failing remote-state query used to drop the
// branch silently; it must now be reported as a scan error naming the branch,
// and the other branches must keep their findings.
func TestGitErrorsAreSurfaced(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/ok", f.daysAgo(100))
	f.pushed("feat/broken", f.daysAgo(100))
	tip := f.repo.Git("rev-parse", "refs/heads/feat/broken")
	got, err := f.detectFailing(func(args []string) bool {
		return args[0] == "rev-list" && slices.Contains(args, tip)
	})
	if err == nil || !strings.Contains(err.Error(), `"feat/broken"`) || !strings.Contains(err.Error(), "simulated git failure") {
		t.Fatalf("error = %v, want one naming the branch and the cause", err)
	}
	if len(got) != 1 || got[0].Ref != "feat/ok" {
		t.Errorf("findings = %+v, want only feat/ok", got)
	}
}

// TestMergedCheckErrorIsSurfaced: an unknown merge state is never treated as
// merged, so the branch is still reported, but the failure is not silent.
func TestMergedCheckErrorIsSurfaced(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("feat/x", f.daysAgo(100))
	got, err := f.detectFailing(func(args []string) bool {
		return args[0] == "merge-base" && slices.Contains(args, "--is-ancestor") && args[len(args)-1] == "refs/remotes/origin/main"
	})
	if err == nil || !strings.Contains(err.Error(), `"feat/x"`) {
		t.Fatalf("error = %v, want one naming the branch", err)
	}
	if len(got) != 1 {
		t.Errorf("findings = %d, want the branch still reported", len(got))
	}
}

// TestLocalUpstreamBranch: an upstream that is a local branch (remote ".") is
// no evidence of a push, so the branch keeps its never_pushed and unpushed
// flags instead of being mistaken for a pushed one.
func TestLocalUpstreamBranch(t *testing.T) {
	f := newFixture(t, true)
	f.branch("feat/local-up", f.daysAgo(100))
	f.repo.Git("branch", "--set-upstream-to=main", "feat/local-up")
	got := f.only()
	if !slices.Contains(got.RiskFlags, findings.RiskNeverPushed) || !slices.Contains(got.RiskFlags, findings.RiskUnpushedCommits) {
		t.Errorf("flags = %v, want never_pushed and unpushed_commits", got.RiskFlags)
	}
	if got.Meta["upstream"] != "main" {
		t.Errorf("meta = %v", got.Meta)
	}
}
