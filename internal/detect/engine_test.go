package detect_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// fakeDetector adapts a function to the Detector interface.
type fakeDetector struct {
	name string
	fn   func(ctx context.Context, t scope.Target, emit func(findings.Finding)) error
}

func (f fakeDetector) Name() string              { return f.name }
func (f fakeDetector) Description() string       { return "fake" }
func (f fakeDetector) Category() detect.Category { return detect.CategoryFiles }
func (f fakeDetector) Detect(ctx context.Context, _ *detect.Env, t scope.Target, emit func(findings.Finding)) error {
	return f.fn(ctx, t, emit)
}

func targets(n int) []scope.Target {
	out := make([]scope.Target, n)
	for i := range out {
		out[i] = scope.Target{Kind: scope.TargetRepo, Path: fmt.Sprintf("/t/%d", i)}
	}
	return out
}

func finding(id string) findings.Finding { return findings.Finding{ID: id} }

func TestRunDedupesByID(t *testing.T) {
	d := fakeDetector{"a", func(_ context.Context, _ scope.Target, emit func(findings.Finding)) error {
		emit(finding("same"))
		emit(finding("same"))
		return nil
	}}
	var streamed int
	got, errs := detect.Run(context.Background(), &detect.Env{}, targets(5), []detect.Detector{d},
		detect.RunOptions{OnFinding: func(findings.Finding) { streamed++ }})
	if len(got) != 1 || streamed != 1 || len(errs) != 0 {
		t.Fatalf("got %d findings, %d streamed, errs %v", len(got), streamed, errs)
	}
}

func TestRunApplies(t *testing.T) {
	var mu sync.Mutex
	var ran []string
	mk := func(name string) detect.Detector {
		return fakeDetector{name, func(_ context.Context, tg scope.Target, _ func(findings.Finding)) error {
			mu.Lock()
			ran = append(ran, name+"@"+tg.Path)
			mu.Unlock()
			return nil
		}}
	}
	applies := func(d detect.Detector, tg scope.Target) bool {
		return d.Name() == "a" || tg.Path == "/t/1"
	}
	detect.Run(context.Background(), &detect.Env{}, targets(3), []detect.Detector{mk("a"), mk("b")},
		detect.RunOptions{Applies: applies})
	if len(ran) != 4 { // a on 3 targets, b on /t/1 only
		t.Fatalf("ran %v", ran)
	}
	for _, r := range ran {
		if strings.HasPrefix(r, "b@") && r != "b@/t/1" {
			t.Errorf("detector b ran where Applies said no: %s", r)
		}
	}
}

func TestRunSerialisesOnFinding(t *testing.T) {
	d := fakeDetector{"a", func(_ context.Context, tg scope.Target, emit func(findings.Finding)) error {
		emit(finding(tg.Path))
		return nil
	}}
	var active, overlap atomic.Int32
	detect.Run(context.Background(), &detect.Env{}, targets(50), []detect.Detector{d}, detect.RunOptions{
		Concurrency: 8,
		OnFinding: func(findings.Finding) {
			if active.Add(1) > 1 {
				overlap.Add(1)
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
		},
	})
	if overlap.Load() != 0 {
		t.Fatalf("OnFinding ran concurrently %d times", overlap.Load())
	}
}

func TestRunConcurrencyBound(t *testing.T) {
	var cur, peak atomic.Int32
	d := fakeDetector{"a", func(_ context.Context, _ scope.Target, _ func(findings.Finding)) error {
		c := cur.Add(1)
		for {
			p := peak.Load()
			if c <= p || peak.CompareAndSwap(p, c) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		cur.Add(-1)
		return nil
	}}
	detect.Run(context.Background(), &detect.Env{}, targets(60), []detect.Detector{d}, detect.RunOptions{Concurrency: 3})
	if peak.Load() > 3 || peak.Load() < 1 {
		t.Fatalf("peak concurrency %d, want 1..3", peak.Load())
	}
}

func TestRunCancellationStopsWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int32
	d := fakeDetector{"a", func(context.Context, scope.Target, func(findings.Finding)) error {
		if ran.Add(1) == 1 {
			cancel()
		}
		return nil
	}}
	_, errs := detect.Run(ctx, &detect.Env{}, targets(1000), []detect.Detector{d}, detect.RunOptions{Concurrency: 2})
	// Only pairs already handed to a worker before the cancel may still run.
	if ran.Load() > 10 {
		t.Fatalf("%d pairs ran after cancellation", ran.Load())
	}
	if len(errs) == 0 || !strings.Contains(errs[len(errs)-1].Message, "scan interrupted") {
		t.Fatalf("missing interruption error: %v", errs)
	}
}

func TestRunAlreadyCancelledRunsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Int32
	d := fakeDetector{"a", func(context.Context, scope.Target, func(findings.Finding)) error {
		ran.Add(1)
		return nil
	}}
	detect.Run(ctx, &detect.Env{}, targets(500), []detect.Detector{d}, detect.RunOptions{Concurrency: 4})
	if ran.Load() != 0 {
		t.Fatalf("%d pairs ran under a cancelled context", ran.Load())
	}
}

func TestRunIsolatesPanics(t *testing.T) {
	boom := fakeDetector{"boom", func(context.Context, scope.Target, func(findings.Finding)) error {
		panic("kaputt")
	}}
	ok := fakeDetector{"ok", func(_ context.Context, tg scope.Target, emit func(findings.Finding)) error {
		emit(finding("ok:" + tg.Path))
		return nil
	}}
	got, errs := detect.Run(context.Background(), &detect.Env{}, targets(3), []detect.Detector{boom, ok}, detect.RunOptions{})
	if len(got) != 3 {
		t.Fatalf("partial report lost: %d findings", len(got))
	}
	if len(errs) != 3 {
		t.Fatalf("want one ScanError per panicking pair, got %v", errs)
	}
	for _, e := range errs {
		if e.Detector != "boom" || !strings.HasPrefix(e.Message, "panic: kaputt") || e.Path == "" {
			t.Errorf("unexpected error %+v", e)
		}
	}
}

func TestRunReportsDetectorErrors(t *testing.T) {
	d := fakeDetector{"a", func(context.Context, scope.Target, func(findings.Finding)) error {
		return fmt.Errorf("nope")
	}}
	_, errs := detect.Run(context.Background(), &detect.Env{}, targets(2), []detect.Detector{d}, detect.RunOptions{})
	if len(errs) != 2 || errs[0].Message != "nope" {
		t.Fatalf("errs %v", errs)
	}
}
