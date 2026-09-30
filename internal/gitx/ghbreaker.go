package gitx

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// waitDelay bounds how long an external command may keep running, or keep its
// output pipes open through a grandchild, after its context ended. Without it
// exec.Cmd.Wait blocks until every holder of the pipe exits, so a deadline
// or Ctrl-C would not stop a scan.
const waitDelay = 2 * time.Second

// ghVerdict is what one gh attempt says about gh as a whole.
type ghVerdict int

const (
	// ghNeutral: the call failed for a reason specific to the repository
	// (no GitHub remote, bad output); other repositories may still work.
	ghNeutral ghVerdict = iota
	// ghHealthy: gh answered.
	ghHealthy
	// ghDown: timeout, missing binary or network failure; every further
	// call would fail the same way and cost a full timeout.
	ghDown
)

// ghBreaker is the scan-wide circuit breaker for gh. Until the first
// conclusive attempt, calls are serialized so one hanging gh costs a single
// timeout instead of one per repository running in parallel; after gh
// answered, calls run concurrently, and a ghDown verdict from any of them
// still opens the breaker; once open every call returns immediately with
// unknown information.
type ghBreaker struct {
	mu    sync.Mutex
	state ghVerdict // ghNeutral means "not known yet"
}

// do runs f unless the breaker is open. A nil breaker just runs f.
func (b *ghBreaker) do(f func() (PRInfo, ghVerdict)) PRInfo {
	if b == nil {
		info, _ := f()
		return info
	}
	b.mu.Lock()
	switch b.state {
	case ghDown:
		b.mu.Unlock()
		return PRInfo{}
	case ghHealthy:
		b.mu.Unlock()
		info, v := f()
		if v == ghDown {
			// gh answered before but hangs or lost the network now; without
			// opening here every remaining repository would cost a timeout.
			b.mu.Lock()
			b.state = ghDown
			b.mu.Unlock()
		}
		return info
	}
	defer b.mu.Unlock()
	info, v := f()
	b.state = v
	return info
}

// networkMarkers are stderr fragments of gh (and Go's net errors) that mean
// the API was unreachable rather than the repository being unsuitable.
var networkMarkers = []string{
	"error connecting", "dial tcp", "no such host", "i/o timeout", "timed out",
	"network is unreachable", "connection refused", "connection reset",
	"could not resolve host", "tls handshake", "temporary failure in name resolution",
}

// classifyGHError decides the verdict of a failed gh call. parent is the
// caller's context: its cancellation (Ctrl-C) says nothing about gh.
func classifyGHError(parent, attempt context.Context, err error) ghVerdict {
	if parent.Err() != nil {
		return ghNeutral
	}
	if attempt.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return ghDown
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.ToLower(string(exitErr.Stderr))
		for _, m := range networkMarkers {
			if strings.Contains(stderr, m) {
				return ghDown
			}
		}
		return ghNeutral
	}
	// Not an exit status: gh is missing or could not be started.
	return ghDown
}
