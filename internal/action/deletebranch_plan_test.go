package action

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// countingGit counts every git process of a run.
type countingGit struct {
	inner gitx.Runner
	n     atomic.Int64
}

func (c *countingGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	c.n.Add(1)
	return c.inner.Run(ctx, dir, args...)
}

// mergedFindings creates n merged branches and returns their findings.
func (fx *branchFixture) mergedFindings(n int) []findings.Finding {
	fx.t.Helper()
	var fs []findings.Finding
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("feat/m%02d", i)
		fx.featureBranch(name)
		fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge "+name, name)
		fs = append(fs, fx.finding(name, "merged-branch", ""))
	}
	return fs
}

// TestPlanSharesBranchStateAcrossFindings: Plan used to re-open the repository
// and list all refs for every finding (about 29 git processes each, before the
// user even confirms). One Plan pass now reads the shared state once.
func TestPlanSharesBranchStateAcrossFindings(t *testing.T) {
	fx := newBranchFixture(t)
	const n = 20
	fs := fx.mergedFindings(n)
	counter := &countingGit{inner: fx.env.Git}
	fx.env.Git = counter

	ex := NewExecutor(Options{Env: fx.env})
	plan := ex.Plan(context.Background(), fs)
	if len(plan.Skipped)+len(plan.Failed) != 0 {
		t.Fatalf("skipped %v failed %v", plan.Skipped, plan.Failed)
	}
	steps := 0
	for _, g := range plan.Groups {
		steps += len(g.Items)
	}
	if steps != n {
		t.Fatalf("planned %d steps, want %d", steps, n)
	}
	// Per finding only the name check and the ancestry check remain (about 7
	// processes per finding before, 2 now).
	if got, limit := counter.n.Load(), int64(4*n+10); got > limit {
		t.Errorf("Plan spawned %d git processes for %d findings, want at most %d", got, n, limit)
	}
}

// TestApplyKeepsLiveChecksPerFinding: the shared snapshot must never reach
// Apply. A branch that moved after Plan is refused by Apply's own evaluation.
func TestApplyKeepsLiveChecksPerFinding(t *testing.T) {
	fx := newBranchFixture(t)
	fs := fx.mergedFindings(2)
	ctx := withPlanSnapshot(context.Background(), fx.env, nil)
	step, err := fx.act.Plan(ctx, fx.env, fs[0])
	if err != nil {
		t.Fatal(err)
	}
	fx.repo.Checkout(fs[0].Ref)
	fx.repo.Commit("late.txt", "x", "late work", testutil.BaseTime.Add(48*time.Hour))
	fx.repo.Checkout("main")
	en, err := fx.act.Apply(context.Background(), fx.env, step)
	if err != nil {
		t.Fatal(err)
	}
	if en.Status != session.StatusSkipped || !fx.branchExists(fs[0].Ref) {
		t.Fatalf("entry %+v, branch exists %v; want a skip that keeps the branch", en, fx.branchExists(fs[0].Ref))
	}
}
