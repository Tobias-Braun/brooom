package mergedbranch_test

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// countRunner counts git processes.
type countRunner struct {
	inner gitx.Runner
	n     atomic.Int64
}

func (c *countRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	c.n.Add(1)
	return c.inner.Run(ctx, dir, args...)
}

// manyBranches creates merged branches (merged with a merge commit) followed
// by unmerged ones that sit on the final main tip, and returns the merged
// names sorted.
func (f *fixture) manyBranches(merged, unmerged int) []string {
	f.t.Helper()
	var names []string
	for i := 0; i < merged; i++ {
		name := fmt.Sprintf("feat/m%02d", i)
		f.feature(name, name+".txt")
		f.merge(name)
		names = append(names, name)
	}
	for i := 0; i < unmerged; i++ {
		f.feature(fmt.Sprintf("agent/u%02d", i), fmt.Sprintf("u%02d.txt", i))
	}
	f.publish()
	slices.Sort(names)
	return names
}

// scanCached runs the detector with a scan cache, like the real pipeline.
func (f *fixture) scanCached(runner gitx.Runner) []findings.Finding {
	f.t.Helper()
	env := *f.env
	env.Git = runner
	env.Repos = gitx.NewCache(runner)
	var out []findings.Finding
	err := f.det.Detect(context.Background(), &env, f.target(f.repo.Dir), func(x findings.Finding) { out = append(out, x) })
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// TestManyBranchesEmitInBranchOrder: the worker pool must not reorder output.
func TestManyBranchesEmitInBranchOrder(t *testing.T) {
	f := newFixture(t)
	want := f.manyBranches(24, 6)
	for run := 0; run < 3; run++ {
		got := refs(f.scanCached(f.env.Git))
		if !slices.Equal(got, want) {
			t.Fatalf("run %d: refs %v, want %v", run, got, want)
		}
	}
}

// TestManyUnmergedBranchesCostFewProcesses is the exec-count regression test:
// every unmerged branch used to cost a merge-base and a rev-list process for
// the squash check even when base had nothing the branch lacks.
func TestManyUnmergedBranchesCostFewProcesses(t *testing.T) {
	f := newFixture(t)
	requireBehindSupport(t, f.env.Git)
	const unmerged = 40
	f.manyBranches(5, unmerged)
	counter := &countRunner{inner: f.env.Git}
	f.scanCached(counter)
	if got, limit := counter.n.Load(), int64(60); got > limit {
		t.Errorf("%d git processes for %d unmerged branches, want at most %d", got, unmerged, limit)
	}
}

func requireBehindSupport(t *testing.T, r gitx.Runner) {
	t.Helper()
	v, err := gitx.GitVersion(context.Background(), r)
	if err != nil || !v.AtLeast(2, 41) {
		t.Skipf("batched ahead/behind needs git 2.41, have %v (%v)", v, err)
	}
}
