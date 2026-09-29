// Package procs answers "is this file currently open by a process?" so that
// logs still being written are flagged instead of suggested for removal.
//
// Detection is best effort and platform specific (Linux: /proc/<pid>/fd,
// macOS: lsof, Windows: Restart Manager). It must never slow a scan down by
// more than a bounded timeout, and when detection is unavailable the answer
// is "unknown", which callers treat as "not known to be open" while noting
// it in evidence.
package procs

import (
	"context"
	"errors"
)

// ErrUnavailable is returned when open-file detection is not possible on
// this system (missing permissions or tools). Callers continue without it.
var ErrUnavailable = errors.New("open-file detection unavailable")

// OpenFiles reports, for each given absolute path, whether any process has
// it open. For a directory, it reports whether any file below it is open.
// Paths must already be symlink-resolved.
func OpenFiles(ctx context.Context, paths []string) (map[string]bool, error) {
	return nil, ErrUnavailable
}
