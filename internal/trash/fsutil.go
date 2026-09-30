package trash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the filesystem helpers shared by every strategy. The OS
// trash implementations (ostrash_<os>.go) reuse them instead of growing their
// own copy logic:
//
//   - checkRemovable validates a path handed to Remove and lstat()s it
//   - treeSize measures an item without following symlinks
//   - moveTree renames, and falls back to copy + verify + delete across devices
//   - copyTree / verifyCopy / removeTree are the building blocks of that
//     fallback and can be used on their own
//
// None of them ever follows a symlink: links are moved, copied, measured and
// removed as links. Hard links are copied as independent files in the
// cross-device fallback.

// renameFunc is the rename primitive used by moveTree. Tests replace it to
// simulate a cross-device failure without needing two filesystems.
var renameFunc = os.Rename

// probeInUse is the pre-copy check for open files of a cross-device move. It
// is a variable so tests can simulate a locked tree on any OS; the default is
// per platform (fsutil_windows.go, fsutil_unix.go).
var probeInUse = defaultProbeInUse

// removeSourceFunc removes the source after a verified cross-device copy.
// Tests replace it to simulate a removal that fails part-way, which cannot be
// provoked reliably with permissions (root ignores them).
var removeSourceFunc = removeTree

// SourceNotRemovedError reports a cross-device move whose copy at Dst was
// completed and verified, but whose source cleanup failed. RemoveAll may have
// deleted part of Src before failing, so Src can no longer be trusted as a
// complete item while Dst is. Dst is therefore never deleted: the item's data
// is complete at Dst and Src holds at most leftovers.
type SourceNotRemovedError struct {
	Src, Dst string
	Err      error
}

func (e *SourceNotRemovedError) Error() string {
	// Paths are quoted with plain quotes rather than %q: %q doubles the
	// backslashes of Windows paths, which breaks copy-pasting them for the
	// manual cleanup this message asks for.
	return fmt.Sprintf(`moved "%s" to "%s", but removing the source failed: %v; the complete item is at "%s" and "%s" may hold leftovers that need manual cleanup`,
		e.Src, e.Dst, e.Err, e.Dst, e.Src)
}

func (e *SourceNotRemovedError) Unwrap() error { return e.Err }

// checkRemovable rejects paths Remove must never touch and returns the Lstat
// info of the item. A missing path yields an error wrapping fs.ErrNotExist so
// callers can treat it as already gone.
func checkRemovable(path string) (fs.FileInfo, error) {
	if path == "" {
		return nil, errors.New("refusing to remove an empty path")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("refusing to remove relative path %q", path)
	}
	clean := filepath.Clean(path)
	if filepath.Dir(clean) == clean {
		return nil, fmt.Errorf("refusing to remove filesystem root %q", path)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot remove %q: %w", path, err)
	}
	return fi, nil
}

// isSymlink reports whether fi describes a symbolic link.
func isSymlink(fi fs.FileInfo) bool { return fi.Mode()&fs.ModeSymlink != 0 }

// treeSize returns the size of the item at path: the lstat size for files and
// symlinks, the recursive sum of regular-file sizes for directories. Symlinks
// inside directories are not followed and do not count.
func treeSize(path string) (int64, error) {
	size, _, err := measureTree(path)
	return size, err
}

// measureTree is treeSize plus the UTF-16 length of the longest descendant
// path relative to path, counting its leading separator (0 for a file or an
// empty directory). The Windows Recycle Bin needs the latter because items
// are re-rooted deeper below $Recycle.Bin (see checkTreeDepth). One walk
// yields both, so measuring depth costs nothing extra.
func measureTree(path string) (size int64, longestRel int, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if !fi.IsDir() {
		return fi.Size(), 0, nil
	}
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, rerr := filepath.Rel(path, p); rerr == nil && rel != "." {
			longestRel = max(longestRel, 1+utf16Len(rel))
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, longestRel, err
}

// moveTree moves src to dst without following symlinks. dst must not exist.
// It tries a rename first; only when that fails because src and dst are on
// different devices does it copy, verify and then remove the source. Any
// failure before the source removal deletes the partial destination and leaves
// src untouched. If the source removal itself fails, the verified copy is kept
// and a *SourceNotRemovedError is returned (see there).
//
// The dst-exists check and the rename are not atomic: on unix rename replaces
// an existing file, so "never overwrites" holds unless another process creates
// dst inside that small window.
func moveTree(ctx context.Context, src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("cannot move %q: destination %q already exists", src, dst)
	}
	err := renameFunc(src, dst)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		return fmt.Errorf("cannot move %q to %q: %w", src, dst, err)
	}
	if err := checkCrossDevice(ctx, src); err != nil {
		return err
	}
	if err := copyTree(ctx, src, dst); err != nil {
		_ = removeTree(dst)
		return fmt.Errorf("cannot copy %q to %q: %w", src, dst, annotateLocked(err, isLockedError))
	}
	if err := verifyCopy(src, dst); err != nil {
		_ = removeTree(dst)
		return fmt.Errorf("cannot move %q: copy verification failed: %w", src, err)
	}
	if err := removeSourceFunc(src); err != nil {
		// The source may already be partly deleted, so the verified copy is
		// the only complete one and must survive.
		return &SourceNotRemovedError{Src: src, Dst: dst, Err: annotateLocked(err, isLockedError)}
	}
	return nil
}

// checkCrossDevice runs the checks that can be made before the first byte
// is copied, so a move that is bound to fail leaves nothing to clean up: the
// tree must consist of things copyTree can reproduce, and (where the
// platform locks open files) nothing in it may be open.
func checkCrossDevice(ctx context.Context, src string) error {
	if err := checkCopyable(src); err != nil {
		return err
	}
	return probeInUse(ctx, src)
}

// checkCopyable walks src without following links and refuses the first
// entry that is neither a directory, a regular file nor a symlink. On Windows
// that is a mount-point junction (Go reports it as an irregular file), which
// pnpm and npm workspaces put into node_modules. Recreating a junction would
// need a raw reparse-point call, so the move is refused up front with a clear
// message instead of failing half way through the copy. Nothing has been
// written when this returns.
func checkCopyable(src string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("cannot move %q across volumes: %w", src, err)
		}
		if t := d.Type(); t.IsDir() || t.IsRegular() || t&fs.ModeSymlink != 0 {
			return nil
		}
		return fmt.Errorf("cannot move %q across volumes: %q is %s, which cannot be copied (nothing was copied and the item was left untouched); set BROOOM_HOME to a directory on the same volume as the item so it can be moved instead", src, p, describeIrregular(p, d.Type()))
	})
}

// annotateLocked rewrites an error that means "a process holds this file
// open" into one that says so and names the file. On Windows a locked file
// surfaces as a bare "Access is denied" or "sharing violation" from the copy
// or the removal, which does not tell the user what to close. Other errors
// are returned unchanged. locked decides per platform which errors count.
func annotateLocked(err error, locked func(error) bool) error {
	if err == nil || !locked(err) {
		return err
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		// Plain quotes, not %q, which would double the backslashes of a
		// Windows path and break copy-pasting it.
		return fmt.Errorf(`"%s" is in use or not permitted: %w`, pe.Path, err)
	}
	return fmt.Errorf("a file is in use or not permitted: %w", err)
}

// checkNotInUse refuses a cross-device move of an item that a process has
// open: the copy would read a file that is being written and, on Windows, the
// source removal would fail part-way and leave a half-removed source that
// blocks undo. openFiles is procs.OpenFiles; "unknown" answers (unavailable
// or incomplete detection) are allowed, like everywhere else in Brooom.
func checkNotInUse(ctx context.Context, src string, openFiles func(context.Context, []string) (map[string]bool, error)) error {
	open, err := openFiles(ctx, []string{src})
	if err != nil && len(open) == 0 {
		return nil //nolint:nilerr // unknown means allowed; the copy and the source removal report real locks
	}
	if open[filepath.Clean(src)] {
		return fmt.Errorf("cannot move %q across volumes: it is in use by another process (a file inside it is open); close it and try again", src)
	}
	return nil
}

// copyTree copies src to dst preserving modes, mtimes and symlinks (recreated
// with their original target, never dereferenced or validated). Sockets,
// devices and fifos make it fail. The context is checked before every entry.
func copyTree(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case isSymlink(fi):
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return createSymlink(src, target, dst)
	case fi.IsDir():
		return copyDir(ctx, src, dst, fi)
	case fi.Mode().IsRegular():
		return copyFile(src, dst, fi)
	default:
		return fmt.Errorf("%q is a special file (%s) and cannot be copied", src, fi.Mode().Type())
	}
}

// copyDir creates dst owner-writable while it is filled, so read-only source
// directories can be copied, and applies the real mode and mtime last.
func copyDir(ctx context.Context, src, dst string, fi fs.FileInfo) error {
	if err := os.Mkdir(dst, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(ctx, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	if err := os.Chmod(dst, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// copyFile copies a regular file, refusing to overwrite dst.
func copyFile(src, dst string, fi fs.FileInfo) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Chmod(fi.Mode().Perm()); err != nil {
		return err
	}
	// The mtime is set after the final write; closing does not change it.
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// verifyCopy checks that dst mirrors src: same entry types and counts,
// regular-file sizes and symlink targets.
func verifyCopy(src, dst string) error {
	sfi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	dfi, err := os.Lstat(dst)
	if err != nil {
		return err
	}
	if sfi.Mode().Type() != dfi.Mode().Type() {
		return fmt.Errorf("%q and %q differ in type", src, dst)
	}
	switch {
	case isSymlink(sfi):
		return verifyLink(src, dst)
	case sfi.IsDir():
		return verifyDir(src, dst)
	case sfi.Size() != dfi.Size():
		return fmt.Errorf("size of %q is %d, copy has %d", src, sfi.Size(), dfi.Size())
	}
	return nil
}

func verifyLink(src, dst string) error {
	st, err := os.Readlink(src)
	if err != nil {
		return err
	}
	dt, err := os.Readlink(dst)
	if err != nil {
		return err
	}
	if st != dt {
		return fmt.Errorf("link %q points to %q, copy points to %q", src, st, dt)
	}
	return sameLinkKind(src, dst)
}

func verifyDir(src, dst string) error {
	se, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	de, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	if len(se) != len(de) {
		return fmt.Errorf("%q has %d entries, copy has %d", src, len(se), len(de))
	}
	for i, e := range se {
		if e.Name() != de[i].Name() {
			return fmt.Errorf("entry %q of %q missing in copy", e.Name(), src)
		}
		if err := verifyCopy(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// removeTree deletes path permanently. A symlink is removed with os.Remove
// so its target is untouched; os.RemoveAll never follows links inside
// directories either. Where the platform reports read-only entries as
// undeletable (Windows) the attribute is cleared and the removal retried.
func removeTree(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if isSymlink(fi) {
		return os.Remove(path)
	}
	err = os.RemoveAll(path)
	if err == nil {
		return nil
	}
	if !clearReadOnly(path) {
		return err
	}
	return os.RemoveAll(path)
}

// isWithin reports whether p lies strictly inside root, compared by path
// components rather than string prefix.
func isWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
