package action

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/procs"
)

// openRecorder counts and records every open-file check.
type openRecorder struct {
	mu    sync.Mutex
	calls [][]string
	open  map[string]bool
	err   error
	// deadline is the remaining budget seen by the last call, 0 if none.
	deadline time.Duration
}

func (r *openRecorder) fn(ctx context.Context, paths []string) (map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), paths...))
	r.deadline = 0
	if dl, ok := ctx.Deadline(); ok {
		r.deadline = time.Until(dl)
	}
	res := map[string]bool{}
	for _, p := range paths {
		res[p] = r.open[p]
	}
	return res, r.err
}

func (fx *trashFixture) plan(fs ...findings.Finding) *Plan {
	fx.t.Helper()
	ex := NewExecutor(Options{Env: fx.env})
	return ex.Plan(context.Background(), fs)
}

func TestPlanChecksOpenFilesOnceForAllFindings(t *testing.T) {
	fx := newTrashFixture(t)
	rec := &openRecorder{}
	fx.setOpen(rec.fn)
	var fs []findings.Finding
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		fs = append(fs, trashFinding(fx.write("proj/"+n+"/out.log", "x")))
	}
	p := fx.plan(fs...)
	if len(rec.calls) != 1 {
		t.Fatalf("OpenFiles called %d times for %d findings, want 1: %v", len(rec.calls), len(fs), rec.calls)
	}
	if len(rec.calls[0]) != len(fs) {
		t.Errorf("batch has %d paths, want %d", len(rec.calls[0]), len(fs))
	}
	if n := len(p.Skipped) + len(p.Failed); n != 0 {
		t.Errorf("skipped/failed = %d, want 0: %+v %+v", n, p.Skipped, p.Failed)
	}
}

func TestPlanBatchedOpenFileStillRefusesOpenPath(t *testing.T) {
	fx := newTrashFixture(t)
	busy := fx.write("proj/busy/out.log", "x")
	idle := fx.write("proj/idle/out.log", "x")
	rec := &openRecorder{open: map[string]bool{busy: true}}
	fx.setOpen(rec.fn)
	p := fx.plan(trashFinding(busy), trashFinding(idle))
	if len(p.Skipped) != 1 || p.Skipped[0].Finding.Path != busy {
		t.Fatalf("skipped = %+v, want only the open path", p.Skipped)
	}
	wantReason(t, p.Skipped[0].Reason, "open by a process")
	if len(p.Groups) != 1 || len(p.Groups[0].Items) != 1 {
		t.Fatalf("groups = %+v, want the idle path planned", p.Groups)
	}
}

func TestPlanBatchNoteReportsIncompleteCheck(t *testing.T) {
	fx := newTrashFixture(t)
	rec := &openRecorder{err: procs.ErrIncomplete}
	fx.setOpen(rec.fn)
	p := fx.plan(trashFinding(fx.write("proj/a/x.log", "x")), trashFinding(fx.write("proj/b/x.log", "x")))
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(rec.calls))
	}
	for _, g := range p.Groups {
		for _, it := range g.Items {
			wantReason(t, it.Step.Description, "open-file check incomplete")
		}
	}
}

func TestPlanBatchSkipsRefusedAndCoveredPaths(t *testing.T) {
	fx := newTrashFixture(t)
	rec := &openRecorder{}
	fx.setOpen(rec.fn)
	outer := fx.mkdir("proj/outer")
	inner := fx.write("proj/outer/inner/out.log", "x")
	other := fx.write("proj/other/out.log", "x")
	other2 := fx.write("proj/other2/out.log", "x")
	outsideRoot := filepath.Join(filepath.Dir(fx.root), "elsewhere", "x.log")
	p := fx.plan(trashFinding(outer), trashFinding(inner), trashFinding(other), trashFinding(other2),
		trashFinding(outsideRoot), trashFinding(fx.root))
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %d, want 1: %v", len(rec.calls), rec.calls)
	}
	got := map[string]bool{}
	for _, c := range rec.calls[0] {
		got[c] = true
	}
	want := map[string]bool{outer: true, other: true, other2: true}
	if len(got) != len(want) {
		t.Fatalf("batch = %v, want exactly %v (no covered, refused or out-of-scope paths)", rec.calls[0], want)
	}
	for w := range want {
		if !got[w] {
			t.Errorf("batch %v misses %s", rec.calls[0], w)
		}
	}
	// The covered inner path was answered from the outer path's result, so
	// no extra single-path call happened.
	if len(rec.calls) != 1 {
		t.Errorf("extra open-file calls: %v", rec.calls)
	}
	if len(p.Skipped) < 3 {
		t.Errorf("want the refused paths and the covered one skipped, got %+v", p.Skipped)
	}
}

func TestPlanCoveredPathFallsBackWhenOuterIsOpen(t *testing.T) {
	fx := newTrashFixture(t)
	outer := fx.mkdir("proj/outer")
	inner := fx.write("proj/outer/inner/out.log", "x")
	other := fx.write("proj/other/out.log", "x")
	rec := &openRecorder{open: map[string]bool{outer: true}}
	fx.setOpen(rec.fn)
	fx.plan(trashFinding(outer), trashFinding(inner), trashFinding(other))
	// Outer is open, so it is refused; the inner path is not vouched for by
	// the batch and gets its own check.
	if len(rec.calls) != 2 || len(rec.calls[1]) != 1 || rec.calls[1][0] != inner {
		t.Fatalf("calls = %v, want the batch plus a single check of %s", rec.calls, inner)
	}
}

func TestPlanSingleFindingUsesPlainCheck(t *testing.T) {
	fx := newTrashFixture(t)
	rec := &openRecorder{}
	fx.setOpen(rec.fn)
	p := fx.plan(trashFinding(fx.write("proj/a/x.log", "x")))
	if len(rec.calls) != 1 || len(p.Groups) != 1 {
		t.Fatalf("calls = %v groups = %v", rec.calls, p.Groups)
	}
}

func TestPlanBatchBudgetScalesWithPaths(t *testing.T) {
	fx := newTrashFixture(t)
	rec := &openRecorder{}
	fx.setOpen(rec.fn)
	var fs []findings.Finding
	for i := range 40 {
		fs = append(fs, trashFinding(fx.write("proj/d"+string(rune('a'+i%26))+string(rune('a'+i/26))+"/x.log", "x")))
	}
	fx.plan(fs...)
	if rec.deadline <= procs.DefaultTimeout {
		t.Errorf("batch budget = %v, want more than the single-path %v", rec.deadline, procs.DefaultTimeout)
	}
}

func TestOutermostPaths(t *testing.T) {
	sep := string(filepath.Separator)
	p := func(s string) string { return sep + filepath.FromSlash(s) }
	got := outermostPaths([]string{p("a/c"), p("a-b"), p("a"), p("a"), p("a/c/d"), p("z")})
	slices.Sort(got)
	want := []string{p("a"), p("a-b"), p("z")}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func wantReason(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%q does not contain %q", got, want)
	}
}
