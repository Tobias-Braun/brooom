package action

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// lsFilesSpy counts `git ls-files` calls and lets a test misbehave from a
// given call on: fail it, or run a hook (to change the repository) first.
type lsFilesSpy struct {
	gitx.Runner
	mu     sync.Mutex
	calls  int
	failAt int
	hookAt int
	hook   func()
}

func (s *lsFilesSpy) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) == 0 || args[0] != "ls-files" {
		return s.Runner.Run(ctx, dir, args...)
	}
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if s.hook != nil && n == s.hookAt {
		s.hook()
	}
	if n == s.failAt {
		return "", errors.New("simulated git failure")
	}
	return s.Runner.Run(ctx, dir, args...)
}

// trackedBatchFixture is a repository with n untracked build directories
// (findings) of which dir "d3" holds a tracked file.
type trackedBatchFixture struct {
	*trashFixture
	repo *testutil.Repo
	spy  *lsFilesSpy
	fs   []findings.Finding
}

func newTrackedBatchFixture(t *testing.T, n int) *trackedBatchFixture {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("README.md", "x")
	repo.WriteFile("build/d3/tracked.bin", "x")
	repo.CommitAll("init", testutil.BaseTime)
	fx := newTrashFixture(t)
	guard, err := scope.NewGuard(filepath.Dir(repo.Dir))
	if err != nil {
		t.Fatal(err)
	}
	fx.env.Guard = guard
	spy := &lsFilesSpy{Runner: fx.env.Git}
	fx.env.Git = spy
	b := &trackedBatchFixture{trashFixture: fx, repo: repo, spy: spy}
	for i := 0; i < n; i++ {
		p := repo.WriteFile(fmt.Sprintf("build/d%d/out.bin", i), "data")
		b.fs = append(b.fs, trashFinding(filepath.Dir(p)))
	}
	return b
}

func (b *trackedBatchFixture) exists(i int) bool {
	_, err := os.Lstat(b.fs[i].Path)
	return !errors.Is(err, fs.ErrNotExist)
}

func (b *trackedBatchFixture) executor(t *testing.T, apply bool) *Executor {
	t.Helper()
	dirs, err := config.EnsureDirs()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	return NewExecutor(Options{
		Apply: apply, Yes: true, Env: b.env, Store: session.NewStore(dirs.Sessions),
		Command: "brooom sweep", IO: IO{Out: &out, Err: &out},
		StdinIsTTY: func() bool { return false },
	})
}

// TestPlanAsksGitForTrackedFilesOncePerRepository is the regression test for
// #217: Plan used to run one `git ls-files` per trash finding.
func TestPlanAsksGitForTrackedFilesOncePerRepository(t *testing.T) {
	b := newTrackedBatchFixture(t, 30)
	p := b.executor(t, false).Plan(context.Background(), b.fs)
	if b.spy.calls != 1 {
		t.Errorf("git ls-files ran %d times for %d findings, want 1", b.spy.calls, len(b.fs))
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Finding.Path != b.fs[3].Path {
		t.Fatalf("skipped = %+v, want only the directory with a tracked file", p.Skipped)
	}
	wantReason(t, p.Skipped[0].Reason, "tracked")
	if got := len(p.Groups[0].Items); got != 29 {
		t.Errorf("planned %d items, want 29", got)
	}
}

// TestApplyChecksTrackedFilesOncePerPlanAndOncePerApply: 30 findings used to
// cost 30 calls in Plan and 30 in Apply.
func TestApplyChecksTrackedFilesOncePerPlanAndOncePerApply(t *testing.T) {
	b := newTrackedBatchFixture(t, 30)
	res, err := b.executor(t, true).Run(context.Background(), b.fs)
	if err != nil {
		t.Fatal(err)
	}
	if b.spy.calls != 2 {
		t.Errorf("git ls-files ran %d times, want 2 (plan, apply)", b.spy.calls)
	}
	if res.Applied != 29 || res.Failed != 0 {
		t.Errorf("applied/failed = %d/%d, want 29/0", res.Applied, res.Failed)
	}
	if !b.exists(3) {
		t.Error("directory with a tracked file was removed")
	}
}

// TestApplyKeepsLiveTrackedRecheck: a file that becomes tracked between the
// plan and the apply must still stop its directory (the apply-time answer is
// taken live, not reused from the plan).
func TestApplyKeepsLiveTrackedRecheck(t *testing.T) {
	b := newTrackedBatchFixture(t, 6)
	b.spy.hookAt = 2
	b.spy.hook = func() { b.repo.Git("add", "build/d1/out.bin") }
	res, err := b.executor(t, true).Run(context.Background(), b.fs)
	if err != nil {
		t.Fatal(err)
	}
	if !b.exists(1) {
		t.Error("directory that became tracked before apply was removed")
	}
	// Tracked files are a skip, not a failure: d3 at plan time, d1 at apply.
	if res.Skipped != 2 || res.Applied != 4 || res.Failed != 0 {
		t.Errorf("applied/skipped/failed = %d/%d/%d, want 4/2/0", res.Applied, res.Skipped, res.Failed)
	}
}

// TestApplyFailsClosedWhenBatchedGitFails: an error at plan time leaves every
// item unknown (skipped); an error at apply time fails every item and
// removes nothing.
func TestApplyFailsClosedWhenBatchedGitFails(t *testing.T) {
	t.Run("plan", func(t *testing.T) {
		b := newTrackedBatchFixture(t, 5)
		b.spy.failAt = 1
		p := b.executor(t, false).Plan(context.Background(), b.fs)
		if len(p.Skipped) != 5 || len(p.Groups) != 0 {
			t.Fatalf("skipped %d, groups %d, want all 5 skipped", len(p.Skipped), len(p.Groups))
		}
		wantReason(t, p.Skipped[0].Reason, "cannot list tracked files")
	})
	t.Run("apply", func(t *testing.T) {
		b := newTrackedBatchFixture(t, 5)
		b.spy.failAt = 2
		res, err := b.executor(t, true).Run(context.Background(), b.fs)
		if err != nil {
			t.Fatal(err)
		}
		if res.Applied != 0 {
			t.Errorf("applied = %d, want 0", res.Applied)
		}
		for i := range b.fs {
			if !b.exists(i) {
				t.Errorf("finding %d was removed although git could not answer", i)
			}
		}
	})
}

// TestBatchedTrackedCheckSkippedForSingleTarget: one target keeps the plain
// per-path check (one call, same verdict).
func TestBatchedTrackedCheckSkippedForSingleTarget(t *testing.T) {
	b := newTrackedBatchFixture(t, 4)
	p := b.executor(t, false).Plan(context.Background(), b.fs[3:4])
	if b.spy.calls != 1 || len(p.Skipped) != 1 {
		t.Errorf("calls = %d, skipped = %d, want 1 and 1", b.spy.calls, len(p.Skipped))
	}
}
