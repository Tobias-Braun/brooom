//go:build unix && !darwin

package trash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// freedesktop implements the freedesktop.org Trash specification 1.0 for
// Linux and the BSDs. Items on the home filesystem go to the home trash,
// items on other filesystems to a per-user trash at that mount's top
// directory, so a move stays a rename. The injectable fields exist for tests:
// temp directories share one device, so a mount lookup has to be faked.
type freedesktop struct {
	homeTrash string
	uid       int
	now       func() time.Time
	deviceOf  func(string) (uint64, error)
	lstat     func(string) (fs.FileInfo, error)
	// ownerOf reports the uid owning a file; injectable because tests cannot
	// create foreign-owned directories without root.
	ownerOf func(fs.FileInfo) (int, bool)
}

// trashLoc is a chosen trash directory. topdir is empty for the home trash,
// whose .trashinfo paths are absolute; topdir trashes record paths relative
// to the mount point, as the spec requires.
type trashLoc struct {
	dir    string
	topdir string
}

// Strategy implements Trasher.
func (f *freedesktop) Strategy() config.TrashStrategy { return config.StrategyTrash }

// Remove implements Trasher. The .trashinfo is created before the item is
// moved, so a crash can leave an info file without an item but never an item
// that the desktop's trash cannot restore.
func (f *freedesktop) Remove(ctx context.Context, path string) (Record, error) {
	fi, err := checkRemovable(path)
	if err != nil {
		return Record{}, err
	}
	real, err := resolveParent(path)
	if err != nil {
		return Record{}, err
	}
	loc, err := f.locate(real)
	if err != nil {
		return Record{}, fmt.Errorf("cannot trash %q: %w", path, err)
	}
	// A size we cannot fully measure (unreadable subdirectory) must not
	// prevent trashing; the partial sum is still informative.
	size, _ := sizeOf(ctx, real)
	now := f.now()
	recorded := real
	if loc.topdir != "" {
		if recorded, err = filepath.Rel(loc.topdir, real); err != nil {
			return Record{}, err
		}
	}
	name, infoPath, err := reserveName(loc.dir, filepath.Base(real), trashInfoContent(recorded, now))
	if err != nil {
		return Record{}, fmt.Errorf("cannot trash %q: %w", path, err)
	}
	stored := filepath.Join(loc.dir, "files", name)
	if err := moveTree(ctx, real, stored); err != nil {
		var partial *SourceNotRemovedError
		// With a verified copy in the trash the info file must stay: it
		// describes the only complete item.
		if !errors.As(err, &partial) {
			_ = os.Remove(infoPath)
		}
		return Record{}, err
	}
	if fi.IsDir() {
		appendDirSize(loc.dir, size, infoPath, name)
	}
	return Record{
		Strategy:     config.StrategyTrash,
		OriginalPath: real,
		StoredPath:   stored,
		InfoPath:     infoPath,
		SizeBytes:    size,
		IsDir:        fi.IsDir(),
		RemovedAt:    now,
		Restorable:   true,
	}, nil
}

// resolveParent resolves symlinks in the parent directory of path but not in
// the final element, so a symlink is trashed as a link.
func resolveParent(path string) (string, error) {
	clean := filepath.Clean(path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", path, err)
	}
	return filepath.Join(parent, filepath.Base(clean)), nil
}

// underTrash reports whether p is root, inside root, or an ancestor of root
// (moving an ancestor into its own trash is impossible and would fail late).
func underTrash(p, root string) bool {
	return p == root || isWithin(root, p) || isWithin(p, root)
}

// locate chooses and prepares the trash directory for the item at real.
func (f *freedesktop) locate(real string) (trashLoc, error) {
	if underTrash(real, f.homeTrash) {
		return trashLoc{}, fmt.Errorf("refusing to trash %q: it is or contains the trash directory %q", real, f.homeTrash)
	}
	if err := f.ensureTrashDir(f.homeTrash, false); err != nil {
		return trashLoc{}, err
	}
	home := trashLoc{dir: f.homeTrash}
	homeDev, err := f.deviceOf(f.homeTrash)
	if err != nil {
		return trashLoc{}, err
	}
	parent := filepath.Dir(real)
	dev, err := f.deviceOf(parent)
	if err != nil {
		return trashLoc{}, err
	}
	if dev == homeDev {
		return home, nil
	}
	return f.locateTopdir(real, parent, home)
}

// locateTopdir prefers a trash on the item's own filesystem and falls back to
// the home trash (a copy across devices) only when none can be created.
func (f *freedesktop) locateTopdir(real, parent string, home trashLoc) (trashLoc, error) {
	top, err := findTopdir(parent, f.deviceOf)
	if err != nil {
		return trashLoc{}, err
	}
	for _, root := range topdirTrashRoots(top, f.uid) {
		if underTrash(real, root) {
			return trashLoc{}, fmt.Errorf("refusing to trash %q: it is or contains the trash directory %q", real, root)
		}
	}
	for _, dir := range topdirCandidates(top, f.uid, f.lstat) {
		if f.ensureTrashDir(dir, true) == nil {
			return trashLoc{dir: dir, topdir: top}, nil
		}
	}
	return home, nil
}

// Restore implements Trasher. It never overwrites: an existing original path
// is a conflict.
func (f *freedesktop) Restore(ctx context.Context, r Record) error {
	if !r.Restorable || r.StoredPath == "" {
		return fmt.Errorf("%w: %q", ErrNotRestorable, r.OriginalPath)
	}
	root, err := f.checkRecord(r)
	if err != nil {
		return err
	}
	if err := checkRestorable(r); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.OriginalPath), 0o755); err != nil {
		return fmt.Errorf("cannot recreate parent of %q: %w", r.OriginalPath, err)
	}
	if err := moveTree(ctx, r.StoredPath, r.OriginalPath); err != nil {
		return err
	}
	if r.InfoPath != "" {
		if err := os.Remove(r.InfoPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("restored %q but cannot remove trash info %q: %w", r.OriginalPath, r.InfoPath, err)
		}
	}
	dropDirSize(root, filepath.Base(r.StoredPath))
	return nil
}

// checkRestorable verifies the trashed copy still exists and the original
// location is free (Lstat, so a dangling symlink also counts as a conflict).
func checkRestorable(r Record) error {
	if _, err := os.Lstat(r.StoredPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: trashed copy %q is gone", ErrNotRestorable, r.StoredPath)
		}
		return err
	}
	_, err := os.Lstat(r.OriginalPath)
	switch {
	case err == nil:
		return fmt.Errorf("%w: %q", ErrRestoreConflict, r.OriginalPath)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return nil
}

// checkRecord validates the paths of a record before Restore touches the
// filesystem and returns the trash directory the item lives in. The manifest
// is user-editable, so the stored path must be a direct child of a files/
// directory of a trash directory and the info path its matching sibling.
func (f *freedesktop) checkRecord(r Record) (string, error) {
	stored := r.StoredPath
	if !filepath.IsAbs(stored) || filepath.Clean(stored) != stored {
		return "", fmt.Errorf("refusing to restore: stored path %q is not a clean absolute path", stored)
	}
	filesDir := filepath.Dir(stored)
	root := filepath.Dir(filesDir)
	if filepath.Base(filesDir) != "files" || !f.isTrashRoot(root) {
		return "", fmt.Errorf("refusing to restore %q: it is not inside a trash files directory", stored)
	}
	wantInfo := filepath.Join(root, "info", filepath.Base(stored)+".trashinfo")
	if r.InfoPath != "" && r.InfoPath != wantInfo {
		return "", fmt.Errorf("refusing to restore %q: info path %q does not belong to it", stored, r.InfoPath)
	}
	if err := f.checkRootOnDisk(root, r.InfoPath != ""); err != nil {
		return "", fmt.Errorf("refusing to restore %q: %w", stored, err)
	}
	orig := r.OriginalPath
	if !filepath.IsAbs(orig) || filepath.Clean(orig) != orig || filepath.Dir(orig) == orig {
		return "", fmt.Errorf("refusing to restore to %q: not a clean absolute non-root path", orig)
	}
	return root, nil
}

// checkRootOnDisk is the filesystem half of checkRecord. A topdir root has to
// pass the full topdir verification (mount point, owner, mode, no symlinks).
// The home trash is the user's own tree and only has to keep files/ (and
// info/, when the record names an info file) as real directories, since
// Restore moves from and deletes inside them and a symlinked files/ would turn
// that into access to an arbitrary directory.
func (f *freedesktop) checkRootOnDisk(root string, withInfo bool) error {
	if top := f.topdirOf(root); top != "" {
		return f.checkTopdirRoot(root, top)
	}
	subs := []string{"files"}
	if withInfo {
		subs = append(subs, "info")
	}
	for _, sub := range subs {
		fi, err := f.lstat(filepath.Join(root, sub))
		if err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%q is not a real directory", filepath.Join(root, sub))
		}
	}
	return nil
}
