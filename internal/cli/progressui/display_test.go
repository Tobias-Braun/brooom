package progressui

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/progress"
)

// syncBuffer is a bytes.Buffer safe for the renderer goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newTestDisplay() (*Display, *syncBuffer) {
	out := &syncBuffer{}
	return New(Options{Out: out, NoColor: true}), out
}

// settledGoroutines waits for the goroutine count to fall back to at most
// want: bubbletea's last spinner tick may still be sleeping for a few
// milliseconds after the program exited.
func settledGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= want {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutine leak: %d running, want at most %d\n%s", n, want, buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// drawn waits until the display has painted a frame containing want; frames
// are flushed at the frame rate, so a very short run may never paint one.
func drawn(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no frame containing %q was drawn: %q", want, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDisplayFullRun(t *testing.T) {
	before := runtime.NumGoroutine()
	d, out := newTestDisplay()
	d.Phase(progress.PhaseScan, 2)
	d.Finding("stale-branch", "/work/api")
	d.Step("stale-branch on /work/api")
	d.Step("worktrees on /work/api")
	drawn(t, out, "Scanning")
	d.Pause()
	afterPause := out.String()
	d.Phase(progress.PhaseApply, 1)
	d.Reclaimed(2_000)
	d.Step("/work/api/dist")
	drawn(t, out, "Applying")
	d.Stop(true)

	got := out.String()
	for _, want := range []string{"Scanning", "Applying", "reclaimed 2.0 kB"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%q", want, got)
		}
	}
	if !strings.Contains(afterPause, "Scanning") {
		t.Errorf("nothing was drawn before the pause: %q", afterPause)
	}
	if strings.Contains(afterPause, "Applying") {
		t.Errorf("the apply phase was drawn before it started: %q", afterPause)
	}
	if strings.Contains(got, "done") {
		t.Errorf("a successful run left a summary line: %q", got[max(0, len(got)-120):])
	}
	// The cursor is hidden while drawing and must be visible again at the end.
	if hide, show := strings.LastIndex(got, "\x1b[?25l"), strings.LastIndex(got, "\x1b[?25h"); hide < 0 || show < hide {
		t.Errorf("cursor not restored (hide at %d, show at %d)", hide, show)
	}
	settledGoroutines(t, before)
}

func TestDisplayDrawsNothingWhilePaused(t *testing.T) {
	d, out := newTestDisplay()
	d.Phase(progress.PhaseScan, 10)
	drawn(t, out, "Scanning")
	d.Pause()
	n := len(out.String())
	// Events keep arriving while paused (a worker finishing late) and must be
	// counted without drawing anything.
	d.Finding("a", "/x")
	d.Step("late")
	time.Sleep(150 * time.Millisecond)
	if got := out.String(); len(got) != n {
		t.Errorf("drew %d bytes while paused: %q", len(got)-n, got[n:])
	}
	d.Phase(progress.PhaseApply, 1)
	d.Stop(false)
	if !strings.Contains(out.String(), "1 findings in 1 detectors") {
		t.Errorf("an event received while paused was lost: %q", out.String())
	}
}

func TestDisplayStopWhilePaused(t *testing.T) {
	for _, tc := range []struct {
		ok   bool
		tail string
	}{
		{true, ""},
		{false, "✗ stopped · scan · 1 findings in 1 detectors\n"},
	} {
		before := runtime.NumGoroutine()
		d, out := newTestDisplay()
		d.Phase(progress.PhaseScan, 1)
		d.Finding("a", "/x")
		d.Pause()
		written := len(out.String())
		d.Stop(tc.ok)
		if tail := out.String()[written:]; tail != tc.tail {
			t.Errorf("Stop(%v) after pause printed %q, want %q", tc.ok, tail, tc.tail)
		}
		settledGoroutines(t, before)
	}
}

func TestDisplayStopFailedRun(t *testing.T) {
	d, out := newTestDisplay()
	d.Phase(progress.PhaseScan, 4)
	d.Stop(false)
	if !strings.Contains(out.String(), "✗ stopped") {
		t.Errorf("no failure summary: %q", out.String())
	}
}

func TestDisplayWithoutPhasePrintsNothing(t *testing.T) {
	before := runtime.NumGoroutine()
	d, out := newTestDisplay()
	d.Pause()
	d.Step("x")
	d.Stop(true)
	if out.String() != "" {
		t.Errorf("a run that never reported a phase drew %q", out.String())
	}
	settledGoroutines(t, before)
}

func TestDisplayStopIsIdempotentAndFinal(t *testing.T) {
	d, out := newTestDisplay()
	d.Phase(progress.PhaseScan, 1)
	d.Stop(true)
	n := len(out.String())
	d.Stop(false)
	d.Pause()
	d.Phase(progress.PhaseApply, 1)
	d.Stop(true)
	if got := out.String(); len(got) != n {
		t.Errorf("output changed after Stop: %q", got[n:])
	}
}

// TestDisplayRepeatedPauseResume restarts the program many times while
// several goroutines keep reporting, the shape of an engine with workers.
// It runs under -race in CI.
func TestDisplayRepeatedPauseResume(t *testing.T) {
	before := runtime.NumGoroutine()
	d, _ := newTestDisplay()
	d.Phase(progress.PhaseScan, 400)
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				d.Step(fmt.Sprintf("w%d-%d", w, i))
				d.Finding("det", fmt.Sprintf("/t/%d", i%3))
				d.Reclaimed(1)
			}
		}()
	}
	for range 5 {
		d.Pause()
		d.Phase(progress.PhaseScan, 400)
	}
	wg.Wait()
	d.Stop(true)
	s := d.snapshot()
	// Phase restarts the step counter (it is called between the pauses), but
	// findings and bytes accumulate over the whole run.
	if s.Findings != 400 || s.Bytes != 400 {
		t.Errorf("events lost across restarts: findings %d, bytes %d, want 400 each", s.Findings, s.Bytes)
	}
	settledGoroutines(t, before)
}

func TestDisplayDefaultsToStderr(t *testing.T) {
	d := New(Options{})
	if d.opts.Out == nil {
		t.Fatal("no default output")
	}
	d.Stop(true) // never started: prints nothing, must not block
}
