package detect_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/progress/progresstest"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

func TestRunReportsScanProgress(t *testing.T) {
	// "a" emits one unique finding per target and a duplicate that must not
	// count; "b" fails on every target, which still finishes its pair.
	a := fakeDetector{"a", func(_ context.Context, tg scope.Target, emit func(findings.Finding)) error {
		emit(finding("a" + tg.Path))
		emit(finding("a" + tg.Path))
		return nil
	}}
	b := fakeDetector{"b", func(context.Context, scope.Target, func(findings.Finding)) error {
		return errors.New("boom")
	}}
	rec := &progresstest.Recorder{}
	got, errs := detect.Run(context.Background(), &detect.Env{}, targets(4), []detect.Detector{a, b},
		detect.RunOptions{Concurrency: 3, Progress: rec})
	if len(got) != 4 || len(errs) != 4 {
		t.Fatalf("findings %d, errors %d, want 4 and 4", len(got), len(errs))
	}
	events := rec.Events()
	if events[0] != "phase:scan/8" {
		t.Errorf("first event %q, want phase:scan/8", events[0])
	}
	if n := rec.Count("step:"); n != 8 {
		t.Errorf("steps = %d, want one per (target, detector) pair (8)", n)
	}
	if n := rec.Count("finding:a@"); n != 4 || rec.Count("finding:") != 4 {
		t.Errorf("finding events = %d (a: %d), want 4 unique findings from a only", rec.Count("finding:"), n)
	}
	for i := range 4 {
		if rec.Count(fmt.Sprintf("finding:a@/t/%d", i)) != 1 {
			t.Errorf("target /t/%d not attributed exactly once: %q", i, events)
		}
	}
}

func TestRunProgressTotalHonoursApplies(t *testing.T) {
	d := fakeDetector{"a", func(context.Context, scope.Target, func(findings.Finding)) error { return nil }}
	rec := &progresstest.Recorder{}
	detect.Run(context.Background(), &detect.Env{}, targets(5), []detect.Detector{d}, detect.RunOptions{
		Applies:  func(_ detect.Detector, tg scope.Target) bool { return tg.Path != "/t/0" },
		Progress: rec,
	})
	if events := rec.Events(); events[0] != "phase:scan/4" || rec.Count("step:") != 4 {
		t.Errorf("events %q, want total 4 and 4 steps", events)
	}
}

func TestRunWithoutProgressStillWorks(t *testing.T) {
	d := fakeDetector{"a", func(_ context.Context, _ scope.Target, emit func(findings.Finding)) error {
		emit(finding("x"))
		return nil
	}}
	got, _ := detect.Run(context.Background(), &detect.Env{}, targets(1), []detect.Detector{d}, detect.RunOptions{})
	if len(got) != 1 {
		t.Fatalf("findings = %d", len(got))
	}
}
