package detect

import (
	"context"
	"runtime"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/findings"
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
}

// Run executes detectors over targets in parallel and returns the unique
// findings (deduplicated by ID, first one wins) and non-fatal errors.
func Run(ctx context.Context, env *Env, targets []scope.Target, detectors []Detector, opts RunOptions) ([]findings.Finding, []findings.ScanError) {
	n := opts.Concurrency
	if n <= 0 {
		n = runtime.NumCPU()
	}
	var (
		mu     sync.Mutex
		seen   = map[string]bool{}
		found  []findings.Finding
		errs   []findings.ScanError
		wg     sync.WaitGroup
		tokens = make(chan struct{}, n)
	)
	emit := func(f findings.Finding) {
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
	}
	for _, t := range targets {
		for _, d := range detectors {
			if opts.Applies != nil && !opts.Applies(d, t) {
				continue
			}
			wg.Add(1)
			go func(t scope.Target, d Detector) {
				defer wg.Done()
				select {
				case tokens <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-tokens }()
				if err := d.Detect(ctx, env, t, emit); err != nil {
					mu.Lock()
					errs = append(errs, findings.ScanError{Detector: d.Name(), Path: t.Path, Message: err.Error()})
					mu.Unlock()
				}
			}(t, d)
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		errs = append(errs, findings.ScanError{Message: "scan interrupted: " + ctx.Err().Error()})
	}
	return found, errs
}
