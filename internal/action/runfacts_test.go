package action

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// tallyRunner counts git invocations by subcommand. It passes stdin through
// so the squash detection runs exactly as with the real runner.
type tallyRunner struct {
	inner *gitx.ExecRunner
	mu    sync.Mutex
	calls map[string]int
}

func (r *tallyRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	r.count(args)
	return r.inner.Run(ctx, dir, args...)
}

func (r *tallyRunner) RunInput(ctx context.Context, dir string, in io.Reader, args ...string) (string, error) {
	r.count(args)
	return r.inner.RunInput(ctx, dir, in, args...)
}

func (r *tallyRunner) count(args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[args[0]]++
}

func (r *tallyRunner) n(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[sub]
}

// pushedSquashMerges creates the branches, pushes them and squash-merges each
// into a pushed main, so every one is deletable without --force. It returns
// their delete-branch findings.
func (fx *branchFixture) pushedSquashMerges(names ...string) []findings.Finding {
	fx.t.Helper()
	for i, name := range names {
		fx.featureBranch(name)
		fx.repo.Git("push", "-q", "origin", name)
		fx.repo.SquashMerge(name, "squash "+name, testutil.BaseTime.Add(time.Duration(i+2)*time.Hour))
	}
	fx.repo.Push("main")
	var fs []findings.Finding
	for _, name := range names {
		fs = append(fs, fx.finding(name, "merged-branch", "squash"))
	}
	return fs
}

// tally swaps the fixture's runner for a counting one.
func (fx *branchFixture) tally() *tallyRunner {
	fx.t.Helper()
	inner, ok := fx.env.Git.(*gitx.ExecRunner)
	if !ok {
		fx.t.Fatalf("fixture runner is %T", fx.env.Git)
	}
	r := &tallyRunner{inner: inner, calls: map[string]int{}}
	fx.env.Git = r
	return r
}

// TestApplyRunSharesRunFacts pins the cost of deleting several branches in one
// run. Every item used to be evaluated three times (plan, re-plan, Apply),
// each time asking gh and git version. Now the plan pass shares one snapshot,
// and the apply pass evaluates each item once and asks gh and git version
// once for the whole run.
func TestApplyRunSharesRunFacts(t *testing.T) {
	fx := newBranchFixture(t)
	fs := fx.pushedSquashMerges("feat/a", "feat/b", "feat/c")
	var gh atomic.Int32
	setGH(t, func(context.Context, string, []string, ...string) ([]byte, error) {
		gh.Add(1)
		return []byte(`[]`), nil
	})
	tally := fx.tally()

	res, err := NewExecutor(Options{
		Env: fx.env, Apply: true, Yes: true,
		Store: session.NewStore(t.TempDir()), StdinIsTTY: func() bool { return false },
	}).Run(context.Background(), fs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != len(fs) {
		t.Fatalf("applied %d, entries %+v, skips %+v", res.Applied, res.Entries, res.Skips)
	}
	for _, f := range fs {
		if fx.branchExists(f.Ref) {
			t.Errorf("%s still exists", f.Ref)
		}
	}
	// One plan pass and one apply run, each asking once; check-ref-format
	// runs once per evaluation (plan and re-plan, none inside Apply).
	want := map[string]int{"gh": 2, "version": 2, "check-ref-format": 2 * len(fs)}
	got := map[string]int{"gh": int(gh.Load()), "version": tally.n("version"), "check-ref-format": tally.n("check-ref-format")}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s ran %d times, want %d", k, got[k], w)
		}
	}
}
