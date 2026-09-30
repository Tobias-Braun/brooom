package detect_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// failingDetector fails every target with err, wrapped the way real
// detectors wrap it.
type failingDetector struct {
	name string
	err  error
}

func (d failingDetector) Name() string              { return d.name }
func (d failingDetector) Description() string       { return "fails" }
func (d failingDetector) Category() detect.Category { return detect.CategoryGit }
func (d failingDetector) Detect(context.Context, *detect.Env, scope.Target, func(findings.Finding)) error {
	return fmt.Errorf("%s: open repo: %w", d.name, d.err)
}

// TestRunReportsDubiousOwnershipOncePerRepository pins the one visible
// "skipped" line per repository, however many detectors hit the same error,
// while ordinary failures stay per detector.
func TestRunReportsDubiousOwnershipOncePerRepository(t *testing.T) {
	unsafe := &gitx.UnsafeRepoError{Dir: "/w/repo", Stderr: "fatal: detected dubious ownership"}
	targets := []scope.Target{{Kind: scope.TargetRepo, Path: "/w/repo"}}
	dets := []detect.Detector{
		failingDetector{"a", unsafe}, failingDetector{"b", unsafe}, failingDetector{"c", errors.New("boom")},
	}
	_, errs := detect.Run(context.Background(), &detect.Env{}, targets, dets, detect.RunOptions{})
	var skipped, other int
	for _, e := range errs {
		switch {
		case strings.HasPrefix(e.Message, findings.SkipPrefix+"dubious ownership"):
			skipped++
			if !strings.Contains(e.Message, "git config --global --add safe.directory /w/repo") || e.Path != "/w/repo" {
				t.Errorf("skip line lacks the hint or path: %+v", e)
			}
		case e.Detector == "c":
			other++
		default:
			t.Errorf("unexpected scan error %+v", e)
		}
	}
	if skipped != 1 || other != 1 {
		t.Fatalf("skipped = %d, other = %d, want 1 and 1 (%+v)", skipped, other, errs)
	}
}
