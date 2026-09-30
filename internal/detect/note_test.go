package detect_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// TestRunClassifiesErrors pins #191: the engine marks plain errors and panics
// as fatal, and errors wrapped with Note, dubious-ownership skips and an
// interruption as non-fatal.
func TestRunClassifiesErrors(t *testing.T) {
	tests := []struct {
		name      string
		fn        func() error
		wantFatal bool
	}{
		{"plain error", func() error { return errors.New("boom") }, true},
		{"panic", func() error { panic("boom") }, true},
		{"note", func() error { return detect.Note(errors.New("partial")) }, false},
		{"wrapped note", func() error { return fmt.Errorf("ctx: %w", detect.Note(errors.New("partial"))) }, false},
		{"unsafe repo skip", func() error { return &gitx.UnsafeRepoError{Dir: "/t/0"} }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := fakeDetector{"x", func(context.Context, scope.Target, func(findings.Finding)) error { return tc.fn() }}
			_, errs := detect.Run(context.Background(), &detect.Env{}, targets(1), []detect.Detector{d}, detect.RunOptions{})
			if len(errs) != 1 || errs[0].Fatal != tc.wantFatal {
				t.Fatalf("errs = %+v, want one with Fatal=%v", errs, tc.wantFatal)
			}
			if findings.HasFatal(errs) != tc.wantFatal {
				t.Errorf("HasFatal = %v", findings.HasFatal(errs))
			}
		})
	}
}

func TestRunInterruptionIsNote(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, errs := detect.Run(ctx, &detect.Env{}, targets(2), []detect.Detector{fakeDetector{"x", nil}}, detect.RunOptions{})
	if len(errs) != 1 || errs[0].Fatal {
		t.Fatalf("errs = %+v, want one non-fatal interruption", errs)
	}
}

func TestNote(t *testing.T) {
	base := errors.New("base")
	if detect.Note(nil) != nil {
		t.Error("Note(nil) must be nil")
	}
	n := detect.Note(base)
	if !detect.IsNote(n) || !errors.Is(n, base) || n.Error() != "base" {
		t.Errorf("Note must keep the message and chain: %v", n)
	}
	if detect.IsNote(base) || detect.IsNote(nil) {
		t.Error("plain errors are not notes")
	}
}
