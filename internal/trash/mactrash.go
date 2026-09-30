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
	"syscall"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// This file is the whole macOS Trash logic. It carries no build tag on
// purpose: only the native NSFileManager call (nativeTrashItem in
// ostrash_darwin.go) is darwin-specific, so everything here, including the
// Finder style naming, the fallback and the Restore refusals, is unit-tested
// on every CI platform through the injectable native/lstat/move functions. On
// other platforms the code is simply unreachable.

// trashAccessDenied is the actionable message for the macOS TCC limitation:
// since 10.15 ~/.Trash is protected, and a CLI without Full Disk Access gets
// EPERM when it inspects or moves items inside it. trashItemAtURL itself
// works, so removal succeeds while restore may not.
const trashAccessDenied = "macOS denies access to the Trash; restore with Finder 'Put Back' or grant Full Disk Access to your terminal"

// defaultNativeTimeout bounds one native trash call. trashItemAtURL has no
// timeout of its own, so a hung volume (an unresponsive network share, a stuck
// disk) would otherwise block RemoveMany forever. Generous, because trashing a
// large directory tree on a slow volume is legitimately slow.
const defaultNativeTimeout = 2 * time.Minute

// errNativePending marks a native trash call that did not return in time. The
// call cannot be cancelled, so it may still complete later: the item may
// already be in the Trash, be moved any moment, or stay where it is. It is
// neither success nor a clean failure, hence no Record and no fallback.
var errNativePending = errors.New("the native trash call did not return in time and may still be pending")

// maxUniqueName bounds the "name N" search so a pathological Trash cannot
// loop forever.
const maxUniqueName = 100000

// nativeTrashFunc moves path into the Trash through the operating system and
// returns the absolute path the item has there. It must not follow a symlink
// at path. A non-nil error means nothing was moved.
type nativeTrashFunc func(path string) (resulting string, err error)

// macTrash trashes through NSFileManager so Finder's "Put Back" works and
// other volumes use their .Trashes, and falls back to moving into ~/.Trash
// when that is not possible.
type macTrash struct {
	home string
	// uid is the current user, the name of the per-volume .Trashes directory.
	uid int
	now func() time.Time
	// native, lstat and move are seams for tests: they replace the
	// NSFileManager call, the Trash inspection (TCC) and the move helper.
	native nativeTrashFunc
	// nativeTimeout bounds one native call; tests shorten it.
	nativeTimeout time.Duration
	lstat         func(string) (fs.FileInfo, error)
	move          func(ctx context.Context, src, dst string) error
	// mkdirAll recreates missing parents on Restore.
	mkdirAll func(path string, perm fs.FileMode) error
}

// newMacTrash returns a macTrash using the real NSFileManager and filesystem.
func newMacTrash(home string) *macTrash {
	return &macTrash{
		home:          home,
		uid:           os.Getuid(),
		now:           time.Now,
		native:        nativeTrashItem,
		nativeTimeout: defaultNativeTimeout,
		lstat:         os.Lstat,
		move:          moveTree,
		mkdirAll:      os.MkdirAll,
	}
}

// Strategy implements Trasher.
func (m *macTrash) Strategy() config.TrashStrategy { return config.StrategyTrash }

// Remove implements Trasher as a batch of one.
func (m *macTrash) Remove(ctx context.Context, path string) (Record, error) {
	recs, errs := m.RemoveMany(ctx, []string{path})
	return recs[0], errs[0]
}

// pendingItem is a validated path waiting for the trash call. Size and type
// are measured before trashing because the item is gone afterwards.
type pendingItem struct {
	idx   int
	path  string
	size  int64
	isDir bool
}

// RemoveMany trashes several paths. The returned slices are as long as paths;
// a failing item never stops the others, with one exception: once a native
// call is left pending (see errNativePending) the remaining items are not
// attempted, because the volume that hung would most likely hang again and
// every further call would leak another blocked goroutine. The error of an item that was moved
// but whose source cleanup failed is returned together with its record.
func (m *macTrash) RemoveMany(ctx context.Context, paths []string) ([]Record, []error) {
	recs := make([]Record, len(paths))
	errs := make([]error, len(paths))
	pending := false
	for i, p := range paths {
		if pending {
			errs[i] = fmt.Errorf("cannot trash %q: skipped because an earlier native trash call is still pending", p)
			continue
		}
		it, err := m.prepare(ctx, i, p)
		if err != nil {
			errs[i] = err
			continue
		}
		m.trashItem(ctx, it, recs, errs)
		pending = errors.Is(errs[i], errNativePending)
	}
	return recs, errs
}

// prepare validates path and measures it.
func (m *macTrash) prepare(ctx context.Context, idx int, path string) (pendingItem, error) {
	if strings.IndexByte(path, 0) >= 0 {
		return pendingItem{}, fmt.Errorf("cannot trash %q: path contains a NUL byte", path)
	}
	if err := checkMacRemovable(path); err != nil {
		return pendingItem{}, err
	}
	fi, err := checkRemovable(path)
	if err != nil {
		return pendingItem{}, err
	}
	size, err := sizeOf(ctx, path)
	if err != nil {
		return pendingItem{}, fmt.Errorf("cannot measure %q: %w", path, err)
	}
	return pendingItem{idx: idx, path: path, size: size, isDir: fi.IsDir() && !isSymlink(fi)}, nil
}

// trashItem trashes one item natively and stores its outcome. A native failure
// means nothing was moved, so the item takes the ~/.Trash fallback, except
// when the caller gave up (cancelled context) or the original has vanished in
// the meantime, in which case a fallback could only fail or hide a lost item.
// A call that timed out (errNativePending) may still move the item, so it is
// reported as an error without a Record and without a fallback: moving the item
// a second time could race with the pending call.
func (m *macTrash) trashItem(ctx context.Context, it pendingItem, recs []Record, errs []error) {
	if cerr := ctx.Err(); cerr != nil {
		errs[it.idx] = fmt.Errorf("cannot trash %q: %w", it.path, cerr)
		return
	}
	resulting, err := m.callNative(ctx, it.path)
	if errors.Is(err, errNativePending) {
		errs[it.idx] = fmt.Errorf("cannot trash %q: %w; check ~/.Trash before retrying (brooom does not know whether the item was moved)", it.path, err)
		return
	}
	if err == nil && !filepath.IsAbs(resulting) {
		err = fmt.Errorf("cannot trash %q: the system returned no usable trash path", it.path)
	}
	if err == nil {
		recs[it.idx] = m.record(it, filepath.Clean(resulting))
		return
	}
	if gone := originalGone(it.path); gone != nil {
		errs[it.idx] = fmt.Errorf("%w; %w", err, gone)
		return
	}
	m.fallback(ctx, it, err, recs, errs)
}

// callNative runs the native trash call in its own goroutine and waits for it
// at most nativeTimeout, or until ctx is done. On expiry it returns
// errNativePending and leaves the goroutine running (a blocked system call
// cannot be interrupted); its late result is discarded through the buffered
// channel, so the goroutine ends as soon as the system call returns.
func (m *macTrash) callNative(ctx context.Context, path string) (string, error) {
	type result struct {
		resulting string
		err       error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := m.native(path)
		ch <- result{r, err}
	}()
	timer := time.NewTimer(m.nativeTimeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res.resulting, res.err
	case <-timer.C:
		return "", errNativePending
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %w", errNativePending, ctx.Err())
	}
}

// originalGone returns a non-nil error when path is no longer present, or
// cannot be inspected, after a failed native call. Both mean the item may
// already sit in the Trash without a Record, so a ~/.Trash fallback (which
// would fail with not-exist) must not be attempted.
func originalGone(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%q is gone and may already be in the Trash (look in ~/.Trash or /Volumes/<volume>/.Trashes/<uid>; brooom cannot undo it)", path)
	}
	return fmt.Errorf("cannot tell whether %q was trashed: %w", path, err)
}

// record builds the Record of a successfully trashed item.
func (m *macTrash) record(it pendingItem, stored string) Record {
	return Record{
		Strategy:     config.StrategyTrash,
		OriginalPath: it.path,
		StoredPath:   stored,
		SizeBytes:    it.size,
		IsDir:        it.isDir,
		RemovedAt:    m.now().UTC(),
		// Access cannot be probed reliably at removal time (TCC); Restore
		// reports a denial explicitly instead.
		Restorable: true,
	}
}

// fallback moves the item into ~/.Trash after the native call failed for it.
// It never deletes permanently.
func (m *macTrash) fallback(ctx context.Context, it pendingItem, cause error, recs []Record, errs []error) {
	stored, err := m.trashToHome(ctx, it)
	var partial *SourceNotRemovedError
	if err == nil || errors.As(err, &partial) {
		recs[it.idx] = m.record(it, stored)
	}
	if err != nil {
		errs[it.idx] = fmt.Errorf("%w; fallback to ~/.Trash failed: %w", cause, err)
	}
}

// trashToHome moves the item into ~/.Trash under a unique Finder-style name.
// The result carries no Put Back metadata, so only brooom's own undo works.
func (m *macTrash) trashToHome(ctx context.Context, it pendingItem) (string, error) {
	if m.home == "" {
		return "", errors.New("home directory unknown")
	}
	dir := filepath.Join(m.home, ".Trash")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", trashDenied(it.path, err)
	}
	name, err := uniqueTrashName(filepath.Base(it.path), it.isDir, func(candidate string) (bool, error) {
		_, err := m.lstat(filepath.Join(dir, candidate))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	})
	if err != nil {
		return "", trashDenied(it.path, err)
	}
	dst := filepath.Join(dir, name)
	if err := m.move(ctx, it.path, dst); err != nil {
		var partial *SourceNotRemovedError
		if errors.As(err, &partial) {
			return dst, err
		}
		return "", trashDenied(it.path, err)
	}
	return dst, nil
}

// trashDenied wraps permission failures with the quarantine hint; other
// errors are only named.
func trashDenied(path string, err error) error {
	if isPermissionErr(err) {
		return fmt.Errorf("cannot move %q into ~/.Trash: %w; use --trash-strategy quarantine instead", path, err)
	}
	return fmt.Errorf("cannot move %q into ~/.Trash: %w", path, err)
}

// Restore implements Trasher. The stored copy must live inside a Trash
// directory; access denials there are reported as ErrNotRestorable with the
// Full Disk Access hint, never as success.
func (m *macTrash) Restore(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := m.checkRestoreRecord(r)
	if err != nil {
		return err
	}
	if err := m.checkTrashRootNotLink(r, root); err != nil {
		return err
	}
	if _, err := m.lstat(r.StoredPath); err != nil {
		return restoreStatErr(r, err)
	}
	if _, err := m.lstat(r.OriginalPath); err == nil {
		return fmt.Errorf("cannot restore %q: %w", r.OriginalPath, ErrRestoreConflict)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot check %q: %w", r.OriginalPath, err)
	}
	if err := m.mkdirAll(filepath.Dir(r.OriginalPath), 0o755); err != nil {
		return fmt.Errorf("cannot recreate parent of %q: %w", r.OriginalPath, err)
	}
	if err := m.move(ctx, r.StoredPath, r.OriginalPath); err != nil {
		if isPermissionErr(err) {
			return trashAccessErr(r, err)
		}
		return fmt.Errorf("cannot restore %q: %w", r.OriginalPath, err)
	}
	return nil
}

// checkRestoreRecord refuses records that would move something from outside
// the user's Trash or to a non-absolute place, and returns the Trash
// directory the stored copy lives in. The manifest is user-editable, so a
// name somewhere in the path is not enough: the stored path must be a clean
// direct child of ~/.Trash or of <volume>/.Trashes/<uid> of the current user,
// the two places the native trash call puts items.
func (m *macTrash) checkRestoreRecord(r Record) (string, error) {
	if r.StoredPath == "" || r.OriginalPath == "" {
		return "", fmt.Errorf("record for %q has no stored path: %w", r.OriginalPath, ErrNotRestorable)
	}
	if !filepath.IsAbs(r.OriginalPath) || !filepath.IsAbs(r.StoredPath) {
		return "", fmt.Errorf("refusing to restore relative path %q from %q", r.OriginalPath, r.StoredPath)
	}
	root := filepath.Dir(r.StoredPath)
	if filepath.Clean(r.StoredPath) != r.StoredPath || !m.isTrashRoot(root) {
		return "", fmt.Errorf("refusing to restore from %q: not directly inside ~/.Trash or a volume's .Trashes/%d", r.StoredPath, m.uid)
	}
	return root, nil
}

// isTrashRoot reports whether root is ~/.Trash or <volume>/.Trashes/<uid>,
// where volume is "/" or a direct child of /Volumes. The comparison is exact:
// the names come from the trash call itself, not from a user.
func (m *macTrash) isTrashRoot(root string) bool {
	if m.home != "" && root == filepath.Join(m.home, ".Trash") {
		return true
	}
	shared := filepath.Dir(root)
	if filepath.Base(root) != strconv.Itoa(m.uid) || filepath.Base(shared) != ".Trashes" {
		return false
	}
	volume := filepath.Dir(shared)
	return volume == "/" || filepath.Dir(volume) == "/Volumes"
}

// checkTrashRootNotLink refuses a Trash directory (and the volume's .Trashes
// above a per-user one) that is a symlink or not a directory: a link there
// would make the "inside the Trash" check name a place brooom never wrote to.
// Access denials by TCC are reported like for the stored copy.
func (m *macTrash) checkTrashRootNotLink(r Record, root string) error {
	dirs := []string{root}
	if filepath.Base(filepath.Dir(root)) == ".Trashes" {
		dirs = append(dirs, filepath.Dir(root))
	}
	for _, d := range dirs {
		fi, err := m.lstat(d)
		if err != nil {
			return restoreStatErr(r, err)
		}
		if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to restore from %q: %q is not a real directory", r.StoredPath, d)
		}
	}
	return nil
}

// restoreStatErr classifies a failed Lstat of the stored copy.
func restoreStatErr(r Record, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%q is no longer in the Trash: %w", r.StoredPath, ErrNotRestorable)
	case isPermissionErr(err):
		return trashAccessErr(r, err)
	}
	return fmt.Errorf("cannot inspect %q: %w", r.StoredPath, err)
}

// trashAccessErr is the TCC error: it wraps ErrNotRestorable and keeps the
// underlying cause.
func trashAccessErr(r Record, cause error) error {
	return fmt.Errorf("cannot restore %q from %q: %s (%w): %w", r.OriginalPath, r.StoredPath, trashAccessDenied, cause, ErrNotRestorable)
}

// isPermissionErr reports EPERM/EACCES style errors. EPERM is what TCC
// returns, and it does not match fs.ErrPermission on every platform.
func isPermissionErr(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

// isTrashDirName reports whether name is ".Trash" or ".Trashes". The
// comparison ignores case because the default macOS volumes are case
// insensitive, so ".trash" addresses the very same directory.
func isTrashDirName(name string) bool {
	return strings.EqualFold(name, ".Trash") || strings.EqualFold(name, ".Trashes")
}

// isInsideTrash reports whether p is a Trash directory or below one. Any path
// component with a Trash name counts, also an unrelated directory that merely
// carries that name: refusing too much is the safe side. Symlinks are not
// resolved here. This is only the Remove-side refusal; Restore does not use
// it and instead pins the exact Trash locations (isTrashRoot) and Lstats them.
func isInsideTrash(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(p)), "/") {
		if isTrashDirName(part) {
			return true
		}
	}
	return false
}

// checkMacRemovable adds the macOS-specific refusals to checkRemovable:
// volume roots and anything in or being a Trash directory.
func checkMacRemovable(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return nil // checkRemovable reports these with its own message
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "/Volumes" || filepath.ToSlash(filepath.Dir(clean)) == "/Volumes" {
		return fmt.Errorf("refusing to remove volume root %q", path)
	}
	if isInsideTrash(clean) {
		return fmt.Errorf("refusing to remove %q: it is a Trash directory or already inside one", path)
	}
	return nil
}

// uniqueTrashName returns name, or "name 2", "name 3" ... (Finder style) for
// the first candidate that does not exist. Files keep their extension
// ("file 2.txt"); directories and dotfiles without one get the number
// appended. exists reports whether a candidate is taken; its error aborts the
// search, since an unreadable Trash must not lead to overwriting.
func uniqueTrashName(name string, isDir bool, exists func(string) (bool, error)) (string, error) {
	stem, ext := name, ""
	if !isDir {
		if e := filepath.Ext(name); e != name {
			stem, ext = strings.TrimSuffix(name, e), e
		}
	}
	for n := 1; n <= maxUniqueName; n++ {
		candidate := name
		if n > 1 {
			candidate = stem + " " + strconv.Itoa(n) + ext
		}
		taken, err := exists(candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free name for %q in the Trash", name)
}
