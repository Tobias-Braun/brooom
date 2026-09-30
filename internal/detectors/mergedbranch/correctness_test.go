package mergedbranch_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// TestFastForwardMergedBranchIsReported covers the common agent flow: commit
// on a branch, `merge --ff-only` it into main, push main. The branch then
// sits on the base tip and was never pushed under its own name, yet it is
// merged clutter, unlike a branch that was just created at the same tip.
func TestFastForwardMergedBranchIsReported(t *testing.T) {
	f := newFixture(t)
	f.repo.Git("checkout", "-q", "-b", "agent-ff", "main")
	f.repo.Commit("ff.txt", "ff", "agent work", at(1))
	f.repo.Checkout("main")
	f.repo.Git("merge", "-q", "--ff-only", "agent-ff")
	f.publish()
	// A branch that was only created at the (new) base tip stays hidden.
	f.repo.Branch("fresh")

	got := f.detect()
	if !slices.Equal(refs(got), []string{"agent-ff"}) {
		t.Fatalf("got %v, want only agent-ff", refs(got))
	}
	x := mustFind(t, got, "agent-ff")
	if x.SuggestedAction.Type != findings.ActionDeleteBranch {
		t.Errorf("action = %+v", x.SuggestedAction)
	}
	if !slices.Contains(x.RiskFlags, findings.RiskNeverPushed) {
		t.Errorf("flags = %v, want never_pushed kept as information", x.RiskFlags)
	}
}

// failingRunner fails git invocations whose first argument matches, which
// simulates a broken repository state for one kind of query.
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

// TestGitErrorsAreSurfaced checks that a branch whose classification fails is
// reported through a joined scan error while the other branches still yield
// findings.
func TestGitErrorsAreSurfaced(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/a", "a.txt")
	f.merge("feat/a")
	f.publish()
	f.env.Git = failingRunner{Runner: f.env.Git, fail: func(args []string) bool {
		return args[0] == "merge-base" && slices.Contains(args, "--is-ancestor")
	}}

	got, err := f.run(f.target(f.repo.Dir))
	if err == nil {
		t.Fatalf("got findings %v and no error; the failing branch must be reported", refs(got))
	}
	if !strings.Contains(err.Error(), `"feat/a"`) || !strings.Contains(err.Error(), "simulated git failure") {
		t.Errorf("error does not name the branch and cause: %v", err)
	}
}

// TestReflogErrorIsSurfacedAndConservative: a failing reflog read keeps the
// branch hidden (it might be fresh) but is not silent.
func TestReflogErrorIsSurfacedAndConservative(t *testing.T) {
	f := newFixture(t)
	f.repo.Git("checkout", "-q", "-b", "agent-ff", "main")
	f.repo.Commit("ff.txt", "ff", "agent work", at(1))
	f.repo.Checkout("main")
	f.repo.Git("merge", "-q", "--ff-only", "agent-ff")
	f.publish()
	f.env.Git = failingRunner{Runner: f.env.Git, fail: func(args []string) bool { return args[0] == "reflog" }}

	got, err := f.run(f.target(f.repo.Dir))
	if err == nil || !strings.Contains(err.Error(), "reflog") {
		t.Fatalf("error = %v, want a reflog error", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want the branch withheld when the reflog is unreadable", refs(got))
	}
}
