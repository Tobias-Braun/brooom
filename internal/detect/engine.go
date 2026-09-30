package detect

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/progress"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// RunOptions configures a scan.
type RunOptions struct {
	// Concurrency bounds how many (target, detector) pairs run at once
	// (0 = number of CPUs).
	Concurrency int
	// Applies decides whether a detector runs for a target (detector
	// toggles, per-root overrides, target kinds). Nil runs every detector
	// on every target.
	Applies func(d Detector, t scope.Target) bool
	// OnFinding is called for every unique finding as soon as it is found,
	// e.g. to stream ndjson. It is called from a single goroutine at a time.
	OnFinding func(findings.Finding)
	// Progress receives the scan phase: one Step per finished (target,
	// detector) pair and one Finding per unique finding. Nil means none.
	Progress progress.Reporter
}

// pair is one unit of work: a detector applied to a target.
type pair struct {
	t scope.Target
	d Detector
}

// Run executes detectors over targets in parallel and returns the unique
// findings (deduplicated by ID, first one wins) and non-fatal errors.
//
// A fixed pool of workers pulls (target, detector) pairs from a channel, so
// the goroutine count is bounded by Concurrency however many targets there
// are. The producer stops as soon as ctx is done and every worker re-checks
// ctx before starting a pair, so a cancelled scan does not keep running
// detectors. A panic inside a detector is recovered and recorded as a
// ScanError, so one faulty detector never loses the partial report.
func Run(ctx context.Context, env *Env, targets []scope.Target, detectors []Detector, opts RunOptions) ([]findings.Finding, []findings.ScanError) {
	n := opts.Concurrency
	if n <= 0 {
		n = runtime.NumCPU()
	}
	var (
		mu    sync.Mutex
		seen  = map[string]bool{}
		found []findings.Finding
		errs  []findings.ScanError
		// unsafeSeen holds the target paths already reported as dubious
		// ownership, so every affected repository yields a single line no
		// matter how many detectors ran on it.
		unsafeSeen = map[string]bool{}
		wg         sync.WaitGroup
	)
	rep := progress.OrNop(opts.Progress)
	// emit records a finding of pair p. The pair is only needed to attribute
	// the progress event, so the detector's own emit stays a plain callback.
	emit := func(p pair, f findings.Finding) {
		mu.Lock()
		defer mu.Unlock()
		if seen[f.ID] {
			return
		}
		seen[f.ID] = true
		found = append(found, f)
		if opts.OnFinding != nil {
			opts.OnFinding(f)
		}
		rep.Finding(p.d.Name(), p.t.Path)
	}
	// addErr records a detector failure under the mutex.
	addErr := func(p pair, err error) {
		mu.Lock()
		defer mu.Unlock()
		errs = appendScanError(errs, unsafeSeen, p, err)
	}
	rep.Phase(progress.PhaseScan, countPairs(targets, detectors, opts.Applies))
	work := make(chan pair)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				if ctx.Err() != nil {
					continue // drain so the producer can finish
				}
				if err := safeDetect(ctx, env, p, func(f findings.Finding) { emit(p, f) }); err != nil {
					addErr(p, err)
				}
				rep.Step(p.d.Name() + " " + p.t.Path)
			}
		}()
	}
produce:
	for _, t := range targets {
		for _, d := range detectors {
			if opts.Applies != nil && !opts.Applies(d, t) {
				continue
			}
			select {
			case work <- pair{t, d}:
			case <-ctx.Done():
				break produce
			}
		}
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil {
		errs = append(errs, findings.ScanError{Message: "scan interrupted: " + ctx.Err().Error()})
	}
	return found, errs
}

// countPairs is the number of (target, detector) pairs Run will execute, the
// total of the scan progress. It only calls Applies, which is a cheap pure
// decision.
func countPairs(targets []scope.Target, detectors []Detector, applies func(Detector, scope.Target) bool) int {
	n := 0
	for _, t := range targets {
		for _, d := range detectors {
			if applies == nil || applies(d, t) {
				n++
			}
		}
	}
	return n
}

// safeDetect runs one detector and converts a panic into an error so the
// remaining pairs and the partial report survive.
func safeDetect(ctx context.Context, env *Env, p pair, emit func(findings.Finding)) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return p.d.Detect(ctx, env, p.t, emit)
}

// appendScanError records a detector failure. A repository git refuses because
// of dubious ownership is reported once per path as a skip that names the
// fix, instead of once per detector. The caller holds the mutex guarding both
// errs and unsafeSeen.
func appendScanError(errs []findings.ScanError, unsafeSeen map[string]bool, p pair, err error) []findings.ScanError {
	var unsafe *gitx.UnsafeRepoError
	if !errors.As(err, &unsafe) {
		return append(errs, findings.ScanError{Detector: p.d.Name(), Path: p.t.Path, Message: err.Error()})
	}
	if unsafeSeen[p.t.Path] {
		return errs
	}
	unsafeSeen[p.t.Path] = true
	return append(errs, findings.ScanError{Path: p.t.Path, Message: findings.SkipPrefix + unsafe.Error()})
}
