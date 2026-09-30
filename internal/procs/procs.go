// Package procs answers "is this file currently open by a process?" so that
// logs still being written are flagged instead of suggested for removal.
//
// Detection is best effort and platform specific. It never signals, kills or
// modifies anything, and it is bounded by a time budget so it can never stall
// a scan for long.
//
// Mechanisms and their limits:
//
//   - Linux reads /proc/<pid>/fd once per call. Processes that cannot be read
//     (other users, hidepid mounts, exited meanwhile) are skipped silently,
//     because otherwise a scan as a normal user would always be incomplete.
//     Memory-mapped files without an fd and working directories are not seen.
//   - macOS runs lsof. Files are checked in batches, directories with the
//     recursive +D option and one time slice each. lsof only reports other
//     users' processes when permitted to. APFS is usually case-insensitive,
//     so names are matched case-insensitively as a fallback.
//   - Windows uses the Restart Manager. It only knows handles it can attribute
//     to a process and does not cover network shares. It cannot say which
//     registered file is locked, so batches are bisected. Directories are
//     enumerated up to a cap of files and levels.
//   - Other platforms report ErrUnavailable.
//
// Every non-nil error means "unknown" for entries that are false; only true
// entries are reliable then. Unknown is never the same as safe.
package procs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrUnavailable is returned when open-file detection is not possible on
// this system (missing permissions or tools). Callers continue without it.
var ErrUnavailable = errors.New("open-file detection unavailable")

// ErrIncomplete is returned together with a partial result when the time
// budget or a per-item cap was hit, so entries that are false are unknown.
// Entries that are true are still reliable.
var ErrIncomplete = errors.New("open-file detection incomplete")

// DefaultTimeout is the budget applied to a call whose context carries no
// deadline. It covers all paths of the call and every subprocess or syscall.
const DefaultTimeout = 3 * time.Second

// OpenFiles reports, for each given path, whether any process has it open.
// For a directory it reports whether any file below it is open.
//
// Paths must be absolute and already symlink-resolved (see scope.Guard);
// they are cleaned and deduplicated, empty entries are ignored and a relative
// entry is an error. Symlinks are treated as plain files and never followed.
// The result has a key for every valid input path: false means "not known to
// be open", and paths that do not exist are false.
//
// If ctx has no deadline, DefaultTimeout applies. On ErrIncomplete the
// partial result is returned as well. Callers must treat any non-nil error as
// "unknown" for entries that are false.
func OpenFiles(ctx context.Context, paths []string) (map[string]bool, error) {
	files, dirs, res, err := classify(paths)
	if err != nil {
		return nil, err
	}
	if len(files)+len(dirs) == 0 {
		return res, nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("%w: %w", ErrIncomplete, ctx.Err())
	}
	err = openFiles(ctx, files, dirs, res)
	if err != nil && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrIncomplete) && ctx.Err() != nil {
		// A mechanism that failed because the budget ran out reports its own
		// error; normalize it so callers only need to know two sentinels.
		return res, fmt.Errorf("%w: %w", ErrIncomplete, ctx.Err())
	}
	return res, err
}

// classify normalizes the input, seeds the result map with false for every
// valid path and splits existing paths into files and directories. Lstat is
// used so that a symlink counts as a file instead of being followed.
func classify(paths []string) (files, dirs []string, res map[string]bool, err error) {
	res = make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, nil, nil, fmt.Errorf("procs: path %q is not absolute", p)
		}
		p = filepath.Clean(p)
		if _, seen := res[p]; seen {
			continue
		}
		res[p] = false
		info, statErr := os.Lstat(p)
		switch {
		case statErr != nil:
			// Missing or unreadable paths cannot be open by a process we
			// could find, so they stay false without an error.
		case info.IsDir():
			dirs = append(dirs, p)
		default:
			files = append(files, p)
		}
	}
	return files, dirs, res, nil
}

// dirPrefix returns dir with a trailing separator so that "/a/b" does not
// match "/a/bc/file".
func dirPrefix(dir string) string {
	if len(dir) > 0 && os.IsPathSeparator(dir[len(dir)-1]) {
		return dir
	}
	return dir + string(filepath.Separator)
}
