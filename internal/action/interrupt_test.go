package action

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// cancelledGit is a Runner whose every call fails the way a git killed by
// Ctrl-C does after the fix: with the context error.
type cancelledGit struct{}

func (cancelledGit) Run(ctx context.Context, _ string, _ ...string) (string, error) {
	return "", fmt.Errorf("git: %w", ctx.Err())
}

// TestCheckTrackedInterruptIsNotForceAdvice: a git call killed by Ctrl-C used
// to become "tracked files cannot be ruled out (use --force to override)".
func TestCheckTrackedInterruptIsNotForceAdvice(t *testing.T) {
	repo := testutil.NewRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := checkTracked(ctx, &Env{Git: cancelledGit{}}, repo.Dir)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrSkipped) {
		t.Fatalf("want a plain context error, got %v", err)
	}
}

func assertInterruptedSkips(t *testing.T, skips []Skip, want int) {
	t.Helper()
	if len(skips) != want {
		t.Fatalf("want %d interrupted skips, got %+v", want, skips)
	}
	for _, s := range skips {
		if s.Reason != "interrupted" {
			t.Errorf("skip reason %q", s.Reason)
		}
	}
}

// ctxAction is an Action whose Plan fails with the context error once ctx is
// done, like a git call that was killed by Ctrl-C.
type ctxAction struct {
	*fakeAction
	onPlan func(n int)
	// mu guards plans; the executor plans findings concurrently.
	mu    sync.Mutex
	plans int
}

func (a *ctxAction) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	a.mu.Lock()
	a.plans++
	n := a.plans
	if a.onPlan != nil {
		a.onPlan(n)
	}
	// Read under the lock, so a call that came before the cancelling one
	// never sees the cancellation.
	err := ctx.Err()
	a.mu.Unlock()
	if err != nil {
		return Step{}, fmt.Errorf("cannot inspect %s: %w", f.Path, err)
	}
	return a.fakeAction.Plan(ctx, env, f)
}

func (fx *fixture) useAction(a Action) {
	fx.opts.Lookup = func(findings.ActionType) (Action, bool) { return a, true }
}

// TestInterruptDuringPlanStopsBeforeConfirmation: planSteps used to keep
// going after Ctrl-C and report every remaining finding as a failed plan
// ("cannot inspect ...: context canceled"), then go on to the prompt.
func TestInterruptDuringPlanStopsBeforeConfirmation(t *testing.T) {
	fx := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &ctxAction{fakeAction: fx.fake(findings.ActionTrash), onPlan: func(n int) {
		if n == 2 {
			cancel() // Ctrl-C arrives while the second finding is inspected.
		}
	}}
	fx.useAction(a)
	var fs []findings.Finding
	for _, name := range strings.Split("abcdefghij", "") {
		fs = append(fs, find("d", findings.ActionTrash, fx.path(name), "", 1))
	}
	res, err := NewExecutor(fx.opts).Run(ctx, fs)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err: %v", err)
	}
	if res == nil || len(res.Failures) != 0 || res.Failed != 0 {
		t.Fatalf("interruption produced failures: %+v", res)
	}
	// Findings are planned by planWorkers workers: the ones already handed
	// to a worker when Ctrl-C arrived may still be asked, no further one.
	if a.plans > 2+planWorkers-1 {
		t.Errorf("planning went on after the interrupt: %d Plan calls", a.plans)
	}
	if len(fx.log) != 0 || len(fx.manifests()) != 0 {
		t.Errorf("something was applied: %v", fx.log)
	}
	assertInterruptedSkips(t, res.Skips, len(fs)-1)
	if strings.Contains(fx.out.String(), "cannot inspect") {
		t.Errorf("bogus plan failures printed:\n%s", fx.out.String())
	}
}

// TestInterruptDuringDryRunPlan: a dry run interrupted while planning must
// not pretend to have a plan either.
func TestInterruptDuringDryRunPlan(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Apply = false })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	fx.useAction(&ctxAction{fakeAction: fx.fake(findings.ActionTrash)})
	res, err := NewExecutor(fx.opts).Run(ctx, []findings.Finding{find("d", findings.ActionTrash, fx.path("a"), "", 1)})
	if !errors.Is(err, ErrInterrupted) || res.Failed != 0 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
}

// TestPlanCtxErrorIsSkipNotFailure: an action that reports the context error
// for the finding it was inspecting must not turn into a failed plan entry.
func TestPlanCtxErrorIsSkipNotFailure(t *testing.T) {
	fx := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	fx.useAction(&ctxAction{fakeAction: fx.fake(findings.ActionTrash)})
	p := NewExecutor(fx.opts).Plan(ctx, []findings.Finding{find("d", findings.ActionTrash, fx.path("a"), "", 1)})
	if len(p.Failed) != 0 || len(p.Skipped) != 1 || p.Skipped[0].Reason != "interrupted" {
		t.Errorf("failed=%v skipped=%v", p.Failed, p.Skipped)
	}
}

// TestReplanIgnoresInterrupt: the re-plan right before Apply used the
// cancellable context, so Ctrl-C between steps turned the step into a failed
// manifest entry that advised --force. It must see an uncancellable context.
func TestReplanIgnoresInterrupt(t *testing.T) {
	fx := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &ctxAction{fakeAction: fx.fake(findings.ActionTrash), onPlan: func(n int) {
		if n == 2 { // the re-plan of the only step
			cancel()
		}
	}}
	fx.useAction(a)
	res, err := NewExecutor(fx.opts).Run(ctx, []findings.Finding{find("d", findings.ActionTrash, fx.path("a"), "", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 || res.Applied != 1 {
		t.Errorf("result %+v", res)
	}
	for _, m := range fx.manifests() {
		for _, e := range m.Entries {
			if e.Status == session.StatusFailed {
				t.Errorf("failed entry %+v", e)
			}
		}
	}
}
