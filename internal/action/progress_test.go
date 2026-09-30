package action

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/progress/progresstest"
	"github.com/Tobias-Braun/brooom/internal/session"
)

func TestExecutorReportsPlanAndApplyProgress(t *testing.T) {
	rec := &progresstest.Recorder{}
	fx := newFixture(t, func(o *Options) { o.Progress = rec })
	fx.fake(findings.ActionTrash)
	a, b := fx.path("repo", "a"), fx.path("repo", "b")
	if _, err := fx.run(
		find("build-artifacts", findings.ActionTrash, a, "", 1000),
		find("build-artifacts", findings.ActionTrash, b, "", 250),
	); err != nil {
		t.Fatal(err)
	}
	// The plan phase first, then a pause before any plan or prompt text, and
	// the apply phase with the bytes of an item reported when it is recorded,
	// just before its step, then a
	// pause again before the summary is printed.
	want := []string{
		"phase:plan/2", "step:" + a, "step:" + b,
		"pause",
		"phase:apply/2", "bytes:1000", "step:" + a, "bytes:250", "step:" + b,
		"pause",
	}
	if got := rec.Events(); !slices.Equal(got, want) {
		t.Fatalf("events\n got %q\nwant %q", got, want)
	}
}

func TestExecutorDryRunPausesAndNeverApplies(t *testing.T) {
	rec := &progresstest.Recorder{}
	fx := newFixture(t, func(o *Options) { o.Apply = false; o.Progress = rec })
	fx.fake(findings.ActionTrash)
	if _, err := fx.run(find("build-artifacts", findings.ActionTrash, fx.path("a"), "", 10)); err != nil {
		t.Fatal(err)
	}
	if rec.Count("phase:apply") != 0 || rec.Count("bytes:") != 0 {
		t.Errorf("a dry run reported apply progress: %q", rec.Events())
	}
	if rec.Count("pause") != 1 {
		t.Errorf("want one pause before the plan text, events %q", rec.Events())
	}
}

func TestExecutorDoesNotCountSkippedAsReclaimed(t *testing.T) {
	rec := &progresstest.Recorder{}
	fx := newFixture(t, func(o *Options) { o.Progress = rec })
	fx.fake(findings.ActionTrash).apply = func(s Step) (session.Entry, error) {
		return session.Entry{Status: session.StatusSkipped, Error: "vanished"}, nil
	}
	if _, err := fx.run(find("build-artifacts", findings.ActionTrash, fx.path("a"), "", 500)); err != nil {
		t.Fatal(err)
	}
	if n := rec.Count("bytes:"); n != 0 {
		t.Errorf("a skipped step reported reclaimed bytes: %q", rec.Events())
	}
	if n := rec.Count("step:"); n != 2 { // plan step + apply step
		t.Errorf("steps = %d, want 2 (plan and apply), events %q", n, rec.Events())
	}
}

func TestExecutorWithoutReporterStillRuns(t *testing.T) {
	fx := newFixture(t, nil)
	fx.fake(findings.ActionTrash)
	res, err := fx.run(find("build-artifacts", findings.ActionTrash, fx.path("a"), "", 10))
	if err != nil || res.Applied != 1 {
		t.Fatalf("applied %d, err %v", res.Applied, err)
	}
}

func TestUndoReportsProgress(t *testing.T) {
	fx := newUndoFixture(t)
	m := fx.manifest(fx.entry("a"), fx.entry("b"))
	rec := &progresstest.Recorder{}
	if _, err := fx.run(m, UndoOptions{Apply: true, Yes: true, Store: fx.store, Progress: rec}); err != nil {
		t.Fatal(err)
	}
	// Entries are undone in reverse order.
	b, a := filepath.Join(fx.root, "b"), filepath.Join(fx.root, "a")
	want := []string{"pause", "phase:undo/2", "step:" + b, "step:" + a, "pause"}
	if got := rec.Events(); !slices.Equal(got, want) {
		t.Fatalf("events\n got %q\nwant %q", got, want)
	}
}

func TestUndoDryRunReportsNoPhase(t *testing.T) {
	fx := newUndoFixture(t)
	m := fx.manifest(fx.entry("a"))
	rec := &progresstest.Recorder{}
	if _, err := fx.run(m, UndoOptions{Progress: rec}); err != nil {
		t.Fatal(err)
	}
	if got := rec.Events(); !slices.Equal(got, []string{"pause"}) {
		t.Fatalf("events %q, want only the pause before the plan text", got)
	}
}
