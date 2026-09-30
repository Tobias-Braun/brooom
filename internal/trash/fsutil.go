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
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !fi.IsDir() {
		return fi.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// moveTree moves src to dst without following symlinks. dst must not exist.
// It tries a rename first; only when that fails because src and dst are on
// different devices does it copy, verify and then remove the source. Any
// failure on the fallback path removes the partial destination and leaves
// src untouched.
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
	if err := copyTree(ctx, src, dst); err != nil {
		_ = removeTree(dst)
		return fmt.Errorf("cannot copy %q to %q: %w", src, dst, err)
	}
	if err := verifyCopy(src, dst); err != nil {
		_ = removeTree(dst)
		return fmt.Errorf("cannot move %q: copy verification failed: %w", src, err)
	}
	if err := removeTree(src); err != nil {
		// Dropping the copy keeps the source authoritative and avoids a
		// duplicated item.
		_ = removeTree(dst)
		return fmt.Errorf("cannot remove %q after copying: %w", src, err)
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
		return os.Symlink(target, dst)
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
	return nil
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
