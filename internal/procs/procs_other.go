//go:build !linux && !darwin && !windows

package procs

import "context"

// openFiles has no mechanism on this platform (BSDs and other unix-likes have
// no lsof fallback yet), so the answer is always unknown.
func openFiles(_ context.Context, _, _ []string, _ map[string]bool) error {
	return ErrUnavailable
}
