package procs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// dirMaxFiles and dirMaxDepth cap the enumeration of a directory on
	// platforms that can only test individual files (Windows).
	dirMaxFiles = 500
	dirMaxDepth = 8
	// lockBatch is how many files one Restart Manager session registers.
	lockBatch = 256
)

// lockedFunc reports whether any of the files is locked by a process. It
// mirrors what the Windows Restart Manager can answer: "somebody holds one of
// these", never "which one". An error wrapping ErrUnavailable is fatal; any
// other error means the batch could not be judged (for example one path was
// rejected).
type lockedFunc func(files []string) (bool, error)

// bisectLocked identifies the locked files in a batch by asking the oracle
// about ever smaller subsets. Files whose subset could not be judged are
// returned as unknown so the caller can report ErrIncomplete. The known
// argument says the batch is already known to contain a locked file, which
// saves a query: if the left half is clean, the right half must be locked.
func bisectLocked(ctx context.Context, files []string, known bool, locked lockedFunc) (hits, unknown []string, err error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	if ctx.Err() != nil {
		return nil, files, fmt.Errorf("%w: %w", ErrIncomplete, ctx.Err())
	}
	if len(files) == 1 {
		return bisectSingle(files, known, locked)
	}
	if !known {
		got, qerr := locked(files)
		if qerr != nil && errors.Is(qerr, ErrUnavailable) {
			return nil, nil, qerr
		}
		if qerr == nil && !got {
			return nil, nil, nil
		}
		// Either the batch is locked or it could not be judged; splitting
		// isolates the culprit in both cases.
		known = qerr == nil
	}
	return bisectHalves(ctx, files, known, locked)
}

// bisectSingle judges a one-element batch: it is a hit, clean or unknown.
func bisectSingle(files []string, known bool, locked lockedFunc) (hits, unknown []string, err error) {
	if known {
		return files, nil, nil
	}
	got, qerr := locked(files)
	switch {
	case qerr != nil && errors.Is(qerr, ErrUnavailable):
		return nil, nil, qerr
	case qerr != nil:
		return nil, files, nil //nolint:nilerr // an unjudgeable file is reported as unknown, not as a failure
	case got:
		return files, nil, nil
	}
	return nil, nil, nil
}

// bisectHalves recurses into both halves of a batch that contains a locked
// file (known) or could not be judged (!known).
func bisectHalves(ctx context.Context, files []string, known bool, locked lockedFunc) (hits, unknown []string, err error) {
	mid := len(files) / 2
	lh, lu, err := bisectLocked(ctx, files[:mid], false, locked)
	if err != nil {
		return lh, append(lu, files[mid:]...), err
	}
	// If the left half is clean and the batch is known to be locked, the
	// right half holds the lock without asking again. If the lock was released
	// between the queries this can report a false positive; that is the safe
	// direction and accepted for the saved query.
	inferred := known && len(lh) == 0 && len(lu) == 0
	rh, ru, err := bisectLocked(ctx, files[mid:], inferred, locked)
	return append(lh, rh...), append(lu, ru...), err
}

// anyLocked tells whether any of files is locked, in chunks of lockBatch. It
// stops at the first hit. Chunks that could not be judged make the answer
// incomplete unless a hit was found.
func anyLocked(ctx context.Context, files []string, locked lockedFunc) (hit, incomplete bool, err error) {
	for start := 0; start < len(files); start += lockBatch {
		if ctx.Err() != nil {
			return false, true, fmt.Errorf("%w: %w", ErrIncomplete, ctx.Err())
		}
		end := min(start+lockBatch, len(files))
		got, qerr := locked(files[start:end])
		switch {
		case qerr != nil && errors.Is(qerr, ErrUnavailable):
			return false, incomplete, qerr
		case qerr != nil:
			// A rejected path poisons the whole chunk; fall back to
			// bisecting so the good files are still judged.
			hits, unk, berr := bisectLocked(ctx, files[start:end], false, locked)
			if berr != nil {
				return len(hits) > 0, true, berr
			}
			if len(hits) > 0 {
				return true, incomplete, nil
			}
			incomplete = incomplete || len(unk) > 0
		case got:
			return true, incomplete, nil
		}
	}
	return false, incomplete, nil
}

// checkBatches marks every locked file of files in res. It returns whether
// some file could not be judged.
func checkBatches(ctx context.Context, files []string, locked lockedFunc, res map[string]bool) (incomplete bool, err error) {
	for start := 0; start < len(files); start += lockBatch {
		end := min(start+lockBatch, len(files))
		hits, unknown, berr := bisectLocked(ctx, files[start:end], false, locked)
		for _, h := range hits {
			res[h] = true
		}
		incomplete = incomplete || len(unknown) > 0
		if berr != nil {
			return true, berr
		}
	}
	return incomplete, nil
}

// listFilesBelow enumerates regular files below dir breadth first, never
// following symlinks or reparse points (anything that is neither a regular
// file nor a plain directory is skipped). Enumeration stops at dirMaxFiles
// files or dirMaxDepth levels; truncated reports that entries were left out.
func listFilesBelow(ctx context.Context, dir string) (files []string, truncated bool) {
	level := []string{dir}
	for depth := 0; len(level) > 0; depth++ {
		var next []string
		for _, d := range level {
			if ctx.Err() != nil {
				return files, true
			}
			regular, subdirs, ok := readDirEntries(d)
			truncated = truncated || !ok
			if len(files)+len(regular) > dirMaxFiles {
				return append(files, regular[:dirMaxFiles-len(files)]...), true
			}
			files = append(files, regular...)
			next = append(next, subdirs...)
		}
		if depth+1 >= dirMaxDepth && len(next) > 0 {
			return files, true
		}
		level = next
	}
	return files, truncated
}

// readDirEntries splits one directory into regular files and plain
// subdirectories. Symlinks, junctions and other reparse points are skipped so
// the walk cannot leave the directory; ok is false when it was unreadable.
func readDirEntries(dir string) (regular, subdirs []string, ok bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, false
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case e.Type().IsRegular():
			regular = append(regular, p)
		case e.IsDir() && e.Type()&os.ModeSymlink == 0 && e.Type()&os.ModeIrregular == 0:
			subdirs = append(subdirs, p)
		}
	}
	return regular, subdirs, true
}

// lockedDirs implements the directory half of the Windows strategy: a
// directory is open when any file found below it is locked. Hitting a cap or
// an unreadable subdirectory without a hit makes the result incomplete.
func lockedDirs(ctx context.Context, dirs []string, locked lockedFunc, res map[string]bool) (incomplete bool, err error) {
	for _, dir := range dirs {
		if ctx.Err() != nil {
			break
		}
		files, truncated := listFilesBelow(ctx, dir)
		hit, inc, qerr := anyLocked(ctx, files, locked)
		if hit {
			res[dir] = true
		}
		if qerr != nil && errors.Is(qerr, ErrUnavailable) {
			return incomplete, qerr
		}
		if !hit && (truncated || inc || qerr != nil) {
			incomplete = true
		}
	}
	return incomplete || ctx.Err() != nil, nil
}

// extendedPath prefixes long Windows paths with \\?\ (or \\?\UNC\ for UNC
// shares) so the Restart Manager accepts them beyond MAX_PATH. Short and
// already prefixed paths are returned unchanged.
func extendedPath(p string) string {
	const maxPath = 260
	if len(p) < maxPath || strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`) {
		return p
	}
	if strings.HasPrefix(p, `\\`) {
		return `\\?\UNC\` + p[2:]
	}
	return `\\?\` + p
}
