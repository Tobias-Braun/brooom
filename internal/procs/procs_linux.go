//go:build linux

package procs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is the procfs mount point; tests point scanProc at a fake tree.
const procRoot = "/proc"

// openFiles scans /proc once and marks every file and directory that has an
// open descriptor pointing at it.
func openFiles(ctx context.Context, files, dirs []string, res map[string]bool) error {
	return scanProc(ctx, procRoot, files, dirs, res)
}

// scanProc walks <root>/<pid>/fd for every numeric pid. Unreadable processes
// are skipped silently (see the package doc), a missing root is
// ErrUnavailable and an expired context is ErrIncomplete.
func scanProc(ctx context.Context, root string, files, dirs []string, res map[string]bool) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("%w: reading %s: %w", ErrUnavailable, root, err)
	}
	fileSet := make(map[string]struct{}, len(files))
	for _, f := range files {
		fileSet[f] = struct{}{}
	}
	prefixes := make([]string, len(dirs))
	for i, d := range dirs {
		prefixes[i] = dirPrefix(d)
	}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrIncomplete, ctx.Err())
		}
		scanFDs(filepath.Join(root, e.Name(), "fd"), fileSet, dirs, prefixes, res)
	}
	return nil
}

// scanFDs inspects the descriptors of one process. Every failure (EACCES for
// other users, ESRCH for a process that just exited) skips the process.
func scanFDs(fdDir string, fileSet map[string]struct{}, dirs, prefixes []string, res map[string]bool) {
	d, err := os.Open(fdDir)
	if err != nil {
		return
	}
	defer d.Close()
	names, _ := d.Readdirnames(-1)
	for _, n := range names {
		target, err := os.Readlink(filepath.Join(fdDir, n))
		if err != nil {
			continue
		}
		markTarget(target, fileSet, dirs, prefixes, res)
	}
}

// markTarget records one descriptor target. Pseudo targets such as
// "socket:[1]" or "anon_inode:x" and unlinked files ("... (deleted)") do not
// name a path on disk and are ignored.
func markTarget(target string, fileSet map[string]struct{}, dirs, prefixes []string, res map[string]bool) {
	if !strings.HasPrefix(target, "/") || strings.HasSuffix(target, " (deleted)") {
		return
	}
	if _, ok := fileSet[target]; ok {
		res[target] = true
	}
	for i, p := range prefixes {
		if strings.HasPrefix(target, p) {
			res[dirs[i]] = true
		}
	}
}
