package trash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// winTrash moves items into the Windows Recycle Bin with SHFileOperationW.
//
// The shell silently deletes permanently what does not fit the bin or sits on
// a volume where the bin is disabled, and that cannot be noticed afterwards.
// Remove therefore refuses such items before the shell is called (see
// decideBinAvailability) and treats every unknown as a refusal.
//
// Observed behaviour of FOF_WANTNUKEWARNING (a nuke situation must end in an
// abort with the source intact, not in a deletion): this could not be
// verified while writing the code, because no Windows machine was available.
// TestNukeSituationKeepsItem exercises it on Windows CI (it shrinks the bin
// limit of the test volume and calls the shell directly) and logs the observed
// outcome; the pre-flight is the safeguard that does not depend on it, and the
// loss detector after the call reports what would still slip through.
type winTrash struct {
	now        func() time.Time
	settings   binSettingsReader
	volumeGUID func(path string) (string, error)
}

// newOSTrasher returns the Windows Recycle Bin implementation.
func newOSTrasher(Options) (Trasher, error) {
	return &winTrash{now: time.Now, settings: registrySettings{}, volumeGUID: volumeGUID}, nil
}

// Strategy implements Trasher.
func (w *winTrash) Strategy() config.TrashStrategy { return config.StrategyTrash }

// Remove implements Trasher: path validation and refusals, the bin
// availability pre-flight, the shell call and finally the lookup of the new
// $I/$R pair. A link is trashed as a link; its target is never touched.
func (w *winTrash) Remove(ctx context.Context, path string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := validateBinPath(path); err != nil {
		return Record{}, err
	}
	fi, err := checkRemovable(path)
	if err != nil {
		return Record{}, err
	}
	link, err := isReparsePoint(path)
	if err != nil {
		return Record{}, fmt.Errorf("cannot inspect %q: %w", path, err)
	}
	size, err := treeSize(path)
	if err != nil {
		return Record{}, fmt.Errorf("cannot measure %q: %w", path, err)
	}
	if err := w.preflight(path, size); err != nil {
		return Record{}, err
	}
	at := w.now()
	if err := w.shellRemove(path, size); err != nil {
		return Record{}, err
	}
	rec := Record{
		Strategy:     config.StrategyTrash,
		OriginalPath: path,
		SizeBytes:    size,
		IsDir:        fi.IsDir() && !link,
		RemovedAt:    at.UTC(),
	}
	return w.recordRemoval(rec, at)
}

// preflight refuses the removal when the bin of the item's volume cannot
// take it. It runs before the shell call because afterwards is too late.
func (w *winTrash) preflight(path string, size int64) error {
	guid, err := w.volumeGUID(path)
	var s binSettings
	if err == nil {
		s, err = w.settings.read(guid)
	}
	return decideBinAvailability(path, s, err, size)
}

// shellRemove calls the shell and turns its result into an error. before is
// the measured size of the item, used to tell an intact item from a partly
// removed one after a failure.
func (w *winTrash) shellRemove(path string, before int64) error {
	from, err := buildFromBuffer([]string{path})
	if err != nil {
		return err
	}
	code, aborted := shellDelete(from)
	if code == 0 && !aborted {
		return nil
	}
	return withIntegrityNote(shellFailure(path, code, aborted), path, before)
}

// shellFailure builds the error for a non-zero result or an aborted
// operation. Sharing and lock violations, and an abort on a path that still
// exists, are reported as a file in use, wrapping the errno.
func shellFailure(path string, code int, aborted bool) error {
	_, statErr := os.Lstat(path)
	switch {
	case isInUseCode(code):
		return fmt.Errorf("cannot move %s to the Recycle Bin: file is in use by another process: %w", path, syscall.Errno(code))
	case code == 0 && aborted && statErr == nil:
		return fmt.Errorf("cannot move %s to the Recycle Bin: file is in use by another process (the operation was aborted): %w", path, syscall.Errno(errorSharingViolation))
	case code == 0:
		return fmt.Errorf("cannot move %s to the Recycle Bin: the operation was aborted", path)
	}
	return fmt.Errorf("cannot move %s to the Recycle Bin: %s (code 0x%X): %w", path, shellErrorMessage(code), code, syscall.Errno(code))
}

// withIntegrityNote verifies by Lstat that the source still exists intact
// after a failed call and says so in the error, or warns if it does not.
func withIntegrityNote(err error, path string, before int64) error {
	if _, statErr := os.Lstat(path); statErr != nil {
		return fmt.Errorf("%w; WARNING: the item no longer exists at that path, check the Recycle Bin", err)
	}
	if after, sizeErr := treeSize(path); sizeErr != nil || after != before {
		return fmt.Errorf("%w; WARNING: the item may have been partly removed", err)
	}
	return fmt.Errorf("%w; the item was left intact", err)
}

// recordRemoval finds the $I/$R pair of a finished removal. It doubles as the
// last-resort loss detector: when the source is gone and a readable bin has
// no matching pair, the item was deleted permanently and that is reported as
// an error. If the bin itself cannot be read, the removal counts as done but
// is flagged non-restorable; the item is still in the bin for Explorer. A bin
// directory that does not exist at all is a permanent deletion.
func (w *winTrash) recordRemoval(rec Record, at time.Time) (Record, error) {
	if _, err := os.Lstat(rec.OriginalPath); err == nil {
		return Record{}, fmt.Errorf("the Recycle Bin reported success for %s but it still exists", rec.OriginalPath)
	}
	dir, entries, err := listBin(rec.OriginalPath)
	if errors.Is(err, fs.ErrNotExist) {
		// The source is gone and the user's bin directory does not exist, so
		// the shell cannot have put it anywhere: a definite loss.
		return Record{}, fmt.Errorf("%s was deleted permanently: it is gone and the Recycle Bin directory does not exist", rec.OriginalPath)
	}
	if err != nil {
		return rec, nil //nolint:nilerr // an unreadable bin only means non-restorable, the removal itself succeeded
	}
	m, ok := chooseRecycled(entries, rec.OriginalPath, at, matchTolerance)
	if !ok {
		return Record{}, fmt.Errorf("%s was deleted permanently: it is gone but no matching item was found in the Recycle Bin", rec.OriginalPath)
	}
	rec.StoredPath = filepath.Join(dir, storedName(m.Name))
	rec.InfoPath = filepath.Join(dir, m.Name)
	rec.RemovedAt = m.Info.DeletedAt
	rec.Restorable = true
	return rec, nil
}

// restoreTolerance is the deletion-time tolerance when a record has no
// StoredPath and the bin is searched by original path.
const restoreTolerance = 2 * time.Second

// Restore implements Trasher. It never overwrites and acts only on items
// directly inside a $Recycle.Bin directory of the original path's volume.
func (w *winTrash) Restore(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Strategy != config.StrategyTrash {
		return fmt.Errorf("%q was not moved to the Recycle Bin: %w", r.OriginalPath, ErrNotRestorable)
	}
	if err := validateBinPath(r.OriginalPath); err != nil {
		return fmt.Errorf("cannot restore to %q: %w", r.OriginalPath, err)
	}
	stored, info, err := w.locate(r)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(r.OriginalPath); err == nil {
		return fmt.Errorf("cannot restore %q: %w", r.OriginalPath, ErrRestoreConflict)
	}
	if err := os.MkdirAll(filepath.Dir(r.OriginalPath), 0o755); err != nil {
		return fmt.Errorf("cannot recreate parent of %q: %w", r.OriginalPath, err)
	}
	if err := moveTree(ctx, stored, r.OriginalPath); err != nil {
		return fmt.Errorf("cannot restore %q: %w", r.OriginalPath, err)
	}
	if err := os.Remove(info); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("restored %q, but could not remove its bin metadata %q: %w", r.OriginalPath, info, err)
	}
	return nil
}

// locate returns the checked $R and $I paths of a record, searching the bin
// by original path and time when the record has no StoredPath.
func (w *winTrash) locate(r Record) (stored, info string, err error) {
	if r.StoredPath == "" {
		return w.search(r)
	}
	if err := checkBinItemPath(r.StoredPath, "$R", r.OriginalPath); err != nil {
		return "", "", err
	}
	if err := checkBinOwner(r.StoredPath); err != nil {
		return "", "", err
	}
	info = filepath.Join(filepath.Dir(r.StoredPath), infoNameOf(filepath.Base(r.StoredPath)))
	if r.InfoPath != "" {
		if err := checkBinItemPath(r.InfoPath, "$I", r.OriginalPath); err != nil {
			return "", "", err
		}
		if err := checkBinInfoPath(r.InfoPath, r.StoredPath); err != nil {
			return "", "", err
		}
		info = r.InfoPath
	}
	if _, err := os.Lstat(r.StoredPath); err != nil {
		return "", "", fmt.Errorf("the Recycle Bin copy %q is gone: %w", r.StoredPath, ErrNotRestorable)
	}
	return r.StoredPath, info, nil
}

// checkBinOwner verifies that the SID directory of a bin path is the current
// user's: brooom only restores from and deletes in its own user's bin.
func checkBinOwner(p string) error {
	sid, err := currentSID()
	if err != nil {
		return err
	}
	if dir := filepath.Base(filepath.Dir(p)); !strings.EqualFold(dir, sid) {
		return fmt.Errorf("refusing %q: it is not in the Recycle Bin of the current user", p)
	}
	return nil
}

// search finds the bin item of a record without StoredPath.
func (w *winTrash) search(r Record) (stored, info string, err error) {
	dir, entries, err := listBin(r.OriginalPath)
	if err != nil {
		return "", "", fmt.Errorf("cannot look up %q in the Recycle Bin: %w: %w", r.OriginalPath, err, ErrNotRestorable)
	}
	m, ok := chooseRecycled(entries, r.OriginalPath, r.RemovedAt, restoreTolerance)
	if !ok {
		return "", "", fmt.Errorf("%q is not in the Recycle Bin: %w", r.OriginalPath, ErrNotRestorable)
	}
	return filepath.Join(dir, storedName(m.Name)), filepath.Join(dir, m.Name), nil
}
