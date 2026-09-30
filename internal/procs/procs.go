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
//     It also reads the cwd, root and exe links of every process, so a shell
//     standing in a directory or a running binary inside it counts as open
//     (links to deleted paths and "/" are ignored). Memory-mapped files
//     without an fd are not seen.
//   - macOS runs lsof. Files are checked in batches, directories with the
//     recursive +D option and one time slice each. lsof only reports other
//     users' processes when permitted to. +D also counts working directories
//     and memory maps of subdirectories, which errs on the side of "open".
//     APFS is usually case-insensitive,
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
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
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
// entry is an error. Symlinks are treated as plain files and are not
// followed by brooom itself; the one exception is Windows, where Restart
// Manager resolves a symlink to its target, so a symlink may be reported open
// when its target is. The result has a key for every valid input path: false means "not known to
// be open", and paths that do not exist are false.
//
// If ctx has no deadline, DefaultTimeout applies. On ErrIncomplete the
// partial result is returned as well. Callers must treat any non-nil error as
// "unknown" for entries that are false.
func OpenFiles(ctx context.Context, paths []string) (map[string]bool, error) {
	// The budget also covers the Lstat calls of classify, which is why it is
	// applied first.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	files, dirs, res, unchecked, err := classify(ctx, paths)
	if err != nil {
		return nil, err
	}
	var statErr error
	if unchecked {
		statErr = fmt.Errorf("%w: some paths could not be inspected", ErrIncomplete)
	}
	if len(files)+len(dirs) == 0 {
		return res, statErr
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("%w: %w", ErrIncomplete, ctx.Err())
	}
	err = normalizeErr(ctx, openFiles(ctx, files, dirs, res))
	if err == nil {
		err = statErr
	}
	return res, err
}

// normalizeErr maps a mechanism that failed because the budget ran out to
// ErrIncomplete so callers only need to know two sentinels.
func normalizeErr(ctx context.Context, err error) error {
	if err != nil && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrIncomplete) && ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrIncomplete, ctx.Err())
	}
	return err
}

// classify normalizes the input, seeds the result map with false for every
// valid path and splits existing paths into files and directories. Lstat is
// used so that a symlink counts as a file instead of being followed. Paths
// that do not exist stay false; paths that cannot be inspected (permission
// denied, budget exhausted) also stay false but set unchecked, because
// unknown is never safe.
func classify(ctx context.Context, paths []string) (files, dirs []string, res map[string]bool, unchecked bool, err error) {
	res = make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, nil, nil, false, fmt.Errorf("procs: path %q is not absolute", p)
		}
		p = filepath.Clean(p)
		if _, seen := res[p]; seen {
			continue
		}
		res[p] = false
		isDir, exists, ok := statPath(ctx, p)
		switch {
		case !ok:
			unchecked = true
		case !exists:
		case isDir:
			dirs = append(dirs, p)
		default:
			files = append(files, p)
		}
	}
	return files, dirs, res, unchecked, nil
}

// statPath Lstats p unless the budget is already spent. ok is false when the
// answer is unknown (budget exhausted or an error other than "does not
// exist"); a missing path is ok with exists false.
func statPath(ctx context.Context, p string) (isDir, exists, ok bool) {
	if ctx.Err() != nil {
		return false, false, false
	}
	info, err := os.Lstat(p)
	switch {
	case err == nil:
		return info.IsDir(), true, true
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return false, false, true
	}
	return false, false, false
}

// dirPrefix returns dir with a trailing separator so that "/a/b" does not
// match "/a/bc/file".
func dirPrefix(dir string) string {
	if len(dir) > 0 && os.IsPathSeparator(dir[len(dir)-1]) {
		return dir
	}
	return dir + string(filepath.Separator)
}
