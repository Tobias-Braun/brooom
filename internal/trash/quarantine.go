package trash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// quarantine moves items into <dir>/<session>/<n>/<basename> and records each
// move in <dir>/<session>/manifest.json. It is the portable, always
// available reversible strategy.
type quarantine struct {
	dir     string
	session string
	now     func() time.Time
	// mu serialises Remove and Restore: the item counter and the manifest are
	// read-modify-written.
	mu sync.Mutex
}

// newQuarantine returns the trasher that moves items into
// <QuarantineDir>/<SessionID>/.
func newQuarantine(opts Options) (Trasher, error) {
	if opts.QuarantineDir == "" {
		return nil, errors.New("quarantine strategy needs a quarantine directory")
	}
	if err := checkSessionID(opts.SessionID); err != nil {
		return nil, err
	}
	return &quarantine{dir: opts.QuarantineDir, session: opts.SessionID, now: time.Now}, nil
}

// checkSessionID rejects ids that are empty or could address anything but a
// single directory name.
func checkSessionID(id string) error {
	if id == "" {
		return errors.New("quarantine strategy needs a session id")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\:`) || strings.ContainsRune(id, 0) {
		return fmt.Errorf("invalid session id %q for quarantine: must be a plain name", id)
	}
	return nil
}

// Strategy implements Trasher.
func (q *quarantine) Strategy() config.TrashStrategy { return config.StrategyQuarantine }

// Remove implements Trasher.
func (q *quarantine) Remove(ctx context.Context, path string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	fi, size, err := q.checkRemoveTarget(ctx, path)
	if err != nil {
		return Record{}, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	sessionDir := filepath.Join(q.dir, q.session)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return Record{}, fmt.Errorf("cannot create quarantine directory for %q: %w", path, err)
	}
	m, err := readManifest(sessionDir, q.session, q.now())
	if err != nil {
		return Record{}, err
	}
	n, stored, err := q.moveIn(ctx, sessionDir, m.nextN(), path)
	// A partial source removal still put a complete copy into quarantine: it
	// is recorded like a normal move and the error is returned alongside.
	var partial *SourceNotRemovedError
	if err != nil && !errors.As(err, &partial) {
		return Record{}, err
	}
	rec := Record{
		Strategy:     config.StrategyQuarantine,
		OriginalPath: path,
		StoredPath:   stored,
		SizeBytes:    size,
		IsDir:        fi.IsDir() && !isSymlink(fi),
		RemovedAt:    q.now().UTC(),
		Restorable:   true,
	}
	m.Items = append(m.Items, QuarantineItem{
		N: n, OriginalPath: path, StoredPath: filepath.ToSlash(filepath.Join(strconv.Itoa(n), filepath.Base(path))),
		RemovedAt: rec.RemovedAt, SizeBytes: size, IsDir: rec.IsDir, IsSymlink: isSymlink(fi),
	})
	if err := writeManifest(sessionDir, m); err != nil {
		return Record{}, q.undoMove(ctx, path, stored, partial, err)
	}
	if partial != nil {
		return rec, fmt.Errorf("quarantined %q, but: %w", path, partial)
	}
	return rec, nil
}

// checkRemoveTarget validates path for Remove and returns its Lstat info and
// size. It refuses paths inside the quarantine directory and ancestors of it
// (the home directory, ~/.brooom), which would be moved into themselves.
func (q *quarantine) checkRemoveTarget(ctx context.Context, path string) (fs.FileInfo, int64, error) {
	fi, err := checkRemovable(path)
	if err != nil {
		return nil, 0, err
	}
	if q.insideQuarantine(path) {
		return nil, 0, fmt.Errorf("refusing to quarantine %q: it is inside the quarantine directory", path)
	}
	if q.containsQuarantine(path) {
		return nil, 0, fmt.Errorf("refusing to quarantine %q: it contains the quarantine directory", path)
	}
	size, err := sizeOf(ctx, path)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot measure %q: %w", path, err)
	}
	return fi, size, nil
}

// undoMove handles a failed manifest write after path was moved to stored.
// Without a manifest entry the item would be orphaned, so it is put back,
// unless the move was partial: then the complete copy is only in quarantine
// and moving it back is not attempted.
func (q *quarantine) undoMove(ctx context.Context, path, stored string, partial *SourceNotRemovedError, werr error) error {
	if partial != nil {
		return fmt.Errorf("%w (and the quarantine manifest could not be updated: %w)", partial, werr)
	}
	if rerr := moveTree(ctx, stored, path); rerr != nil {
		return fmt.Errorf("%w (and moving %q back failed: %w)", werr, path, rerr)
	}
	_ = os.Remove(filepath.Dir(stored))
	return werr
}

// moveIn moves path into a fresh <n> directory, skipping counters whose
// directory already exists on disk (for example after a manifest was lost).
func (q *quarantine) moveIn(ctx context.Context, sessionDir string, n int, path string) (int, string, error) {
	for {
		nDir := filepath.Join(sessionDir, strconv.Itoa(n))
		err := os.Mkdir(nDir, 0o700)
		if errors.Is(err, fs.ErrExist) {
			n++
			continue
		}
		if err != nil {
			return 0, "", fmt.Errorf("cannot create quarantine directory for %q: %w", path, err)
		}
		stored := filepath.Join(nDir, filepath.Base(path))
		if err := moveTree(ctx, path, stored); err != nil {
			var partial *SourceNotRemovedError
			if errors.As(err, &partial) {
				// The <n> directory holds the complete copy; keep it.
				return n, stored, err
			}
			_ = os.Remove(nDir)
			return 0, "", fmt.Errorf("cannot quarantine %q: %w", path, err)
		}
		return n, stored, nil
	}
}

// insideQuarantine reports whether path lies within the quarantine directory,
// judged both literally and after resolving symlinks in the directory and in
// the path's parent (the path itself may be a link and is not followed).
func (q *quarantine) insideQuarantine(path string) bool {
	roots := []string{filepath.Clean(q.dir)}
	if r, err := filepath.EvalSymlinks(q.dir); err == nil {
		roots = append(roots, r)
	}
	candidates := []string{filepath.Clean(path)}
	if p, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		candidates = append(candidates, filepath.Join(p, filepath.Base(path)))
	}
	for _, r := range roots {
		for _, c := range candidates {
			if c == r || isWithin(r, c) {
				return true
			}
		}
	}
	return false
}

// containsQuarantine reports whether the quarantine directory lies inside
// path, that is whether path is an ancestor such as the home directory.
// Quarantining it would try to move the quarantine into itself.
func (q *quarantine) containsQuarantine(path string) bool {
	roots := []string{filepath.Clean(q.dir)}
	if r, err := filepath.EvalSymlinks(q.dir); err == nil {
		roots = append(roots, r)
	}
	candidates := []string{filepath.Clean(path)}
	if p, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		candidates = append(candidates, filepath.Join(p, filepath.Base(path)))
	}
	for _, r := range roots {
		for _, c := range candidates {
			if isWithin(c, r) {
				return true
			}
		}
	}
	return false
}

// storedLocation is a validated StoredPath.
type storedLocation struct {
	sessionDir string
	session    string
	n          int
	path       string
}

// checkStoredPath validates a record's StoredPath before anything is moved.
// Records come from user-editable session manifests, so a forged path must
// never make Restore move an arbitrary file. The path must be exactly
// <QuarantineDir>/<session>/<n>/<name> by path components, and neither the
// session directory nor the <n> directory may be a symlink. The session
// need not be this trasher's own: undo runs in a later process with a new
// session id.
func (q *quarantine) checkStoredPath(stored string) (storedLocation, error) {
	bad := func(format string, a ...any) (storedLocation, error) {
		return storedLocation{}, fmt.Errorf("refusing to restore from %q: %s: %w", stored, fmt.Sprintf(format, a...), ErrNotRestorable)
	}
	if stored == "" || !filepath.IsAbs(stored) {
		return bad("not an absolute path")
	}
	root, err := filepath.EvalSymlinks(q.dir)
	if err != nil {
		return bad("quarantine directory unavailable")
	}
	parts := splitStored(stored, []string{filepath.Clean(q.dir), root})
	if len(parts) != 3 {
		return bad("not inside <quarantine>/<session>/<n>/")
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 1 || checkSessionID(parts[0]) != nil {
		return bad("malformed session or item directory")
	}
	sessionDir := filepath.Join(root, parts[0])
	for _, d := range []string{sessionDir, filepath.Join(sessionDir, parts[1])} {
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() || isSymlink(fi) {
			return bad("%q is not a plain directory", d)
		}
	}
	return storedLocation{sessionDir: sessionDir, session: parts[0], n: n, path: filepath.Join(sessionDir, parts[1], parts[2])}, nil
}

// splitStored returns the components of stored below the first of roots that
// contains it, or nil when none does.
func splitStored(stored string, roots []string) []string {
	clean := filepath.Clean(stored)
	for _, base := range roots {
		if !isWithin(base, clean) {
			continue
		}
		rel, _ := filepath.Rel(base, clean)
		return strings.Split(filepath.ToSlash(rel), "/")
	}
	return nil
}

// Restore implements Trasher. It never overwrites and does not consult the
// manifest to authorise the move: the containment check is the boundary.
func (q *quarantine) Restore(ctx context.Context, r Record) error {
	loc, err := q.checkRestoreTarget(r)
	if err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.OriginalPath), 0o755); err != nil {
		return fmt.Errorf("cannot recreate parent of %q: %w", r.OriginalPath, err)
	}
	err = moveTree(ctx, loc.path, r.OriginalPath)
	var partial *SourceNotRemovedError
	if err != nil && !errors.As(err, &partial) {
		return fmt.Errorf("cannot restore %q: %w", r.OriginalPath, err)
	}
	// The dir is empty after a clean move; a failure to remove it is harmless
	// (and expected after a partial one, which leaves leftovers in it).
	_ = os.Remove(filepath.Dir(loc.path))
	// After a partial removal the item is complete at its original path, so
	// the manifest must stop listing it either way.
	derr := dropFromManifest(loc, r.OriginalPath)
	switch {
	case partial != nil && derr != nil:
		return fmt.Errorf("restored %q, but: %w (and %w)", r.OriginalPath, partial, derr)
	case partial != nil:
		return fmt.Errorf("restored %q, but: %w", r.OriginalPath, partial)
	}
	return derr
}

// checkRestoreTarget validates a record for Restore and returns the checked
// location of the quarantined copy. It refuses to overwrite: an existing
// original path is a conflict.
func (q *quarantine) checkRestoreTarget(r Record) (storedLocation, error) {
	if !r.Restorable || r.Strategy != config.StrategyQuarantine {
		return storedLocation{}, fmt.Errorf("%q was not quarantined: %w", r.OriginalPath, ErrNotRestorable)
	}
	if r.OriginalPath == "" || !filepath.IsAbs(r.OriginalPath) {
		return storedLocation{}, fmt.Errorf("cannot restore to %q: not an absolute path", r.OriginalPath)
	}
	loc, err := q.checkStoredPath(r.StoredPath)
	if err != nil {
		return storedLocation{}, err
	}
	if _, err := os.Lstat(loc.path); err != nil {
		return storedLocation{}, fmt.Errorf("quarantined copy %q is gone: %w", r.StoredPath, ErrNotRestorable)
	}
	if _, err := os.Lstat(r.OriginalPath); err == nil {
		return storedLocation{}, fmt.Errorf("cannot restore %q: %w", r.OriginalPath, ErrRestoreConflict)
	}
	return loc, nil
}

// dropFromManifest removes a restored item from its session manifest. A
// missing manifest is fine (nothing to update); any other failure is
// reported although the restore itself succeeded.
func dropFromManifest(loc storedLocation, restored string) error {
	m, err := readManifest(loc.sessionDir, loc.session, time.Time{})
	if err != nil {
		return fmt.Errorf("restored %q but could not update quarantine manifest: %w", restored, err)
	}
	before := len(m.Items)
	m.drop(loc.n)
	if len(m.Items) == before {
		return nil
	}
	if err := writeManifest(loc.sessionDir, m); err != nil {
		return fmt.Errorf("restored %q but could not update quarantine manifest: %w", restored, err)
	}
	return nil
}
