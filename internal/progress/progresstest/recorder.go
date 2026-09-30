// Package progresstest holds a recording progress.Reporter for the tests of
// the packages that report progress.
package progresstest

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/progress"
)

// Recorder is a progress.Reporter that keeps every event in order, in the
// compact text form "phase:scan/6", "step:label", "finding:detector@target",
// "bytes:n" and "pause". It is safe for concurrent use.
type Recorder struct {
	mu     sync.Mutex
	events []string
}

var _ progress.Reporter = (*Recorder)(nil)

func (r *Recorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

// Phase implements progress.Reporter.
func (r *Recorder) Phase(p progress.Phase, total int) { r.add("phase:%s/%d", p, total) }

// Step implements progress.Reporter.
func (r *Recorder) Step(label string) { r.add("step:%s", label) }

// Finding implements progress.Reporter.
func (r *Recorder) Finding(detector, target string) { r.add("finding:%s@%s", detector, target) }

// Reclaimed implements progress.Reporter.
func (r *Recorder) Reclaimed(bytes int64) { r.add("bytes:%d", bytes) }

// Pause implements progress.Reporter.
func (r *Recorder) Pause() { r.add("pause") }

// Events returns a copy of the recorded events.
func (r *Recorder) Events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// Count returns how many recorded events start with prefix.
func (r *Recorder) Count(prefix string) int {
	n := 0
	for _, e := range r.Events() {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}
