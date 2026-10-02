//go:build unix && !darwin

package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// homeTrashDir returns the home trash directory: $XDG_DATA_HOME/Trash, or
// $HOME/.local/share/Trash. Per the XDG base directory spec an empty or
// non-absolute XDG_DATA_HOME must be ignored. The environment is injected so
// tests do not depend on the process environment.
func homeTrashDir(getenv func(string) string) (string, error) {
	if x := getenv("XDG_DATA_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "Trash"), nil
	}
	if h := getenv("HOME"); h != "" && filepath.IsAbs(h) {
		return filepath.Join(h, ".local", "share", "Trash"), nil
	}
	return "", errors.New("cannot locate the trash: neither XDG_DATA_HOME nor HOME is set to an absolute path")
}

// deviceOf returns the device id of the filesystem holding path, without
// following a final symlink. The generic helper avoids a per-OS Stat_t.Dev
// type switch (uint64, int32 or uint32 depending on the BSD).
func deviceOf(path string) (uint64, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("no device information for %q", path)
	}
	return toUint64(st.Dev), nil
}

func toUint64[T ~int32 | ~uint32 | ~int64 | ~uint64](v T) uint64 { return uint64(v) }

// findTopdir returns the mount point of the filesystem holding dir by walking
// up until the device id changes. The result is the highest ancestor still on
// dir's device.
func findTopdir(dir string, dev func(string) (uint64, error)) (string, error) {
	want, err := dev(dir)
	if err != nil {
		return "", err
	}
	cur := dir
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur, nil
		}
		pd, err := dev(parent)
		if err != nil {
			return "", err
		}
		if pd != want {
			return cur, nil
		}
		cur = parent
	}
}

// topdirCandidates lists the per-user trash directories to try on a non-home
// device, in order of preference. $topdir/.Trash/$uid is only offered when
// .Trash is a real directory (an Lstat, so a symlink does not qualify) with
// the sticky bit; anything else in its place is never written to.
// $topdir/.Trash-$uid is always the last candidate.
func topdirCandidates(topdir string, uid int, lstat func(string) (fs.FileInfo, error)) []string {
	var out []string
	shared := filepath.Join(topdir, ".Trash")
	if fi, err := lstat(shared); err == nil && fi.IsDir() && fi.Mode()&fs.ModeSticky != 0 {
		out = append(out, filepath.Join(shared, strconv.Itoa(uid)))
	}
	return append(out, filepath.Join(topdir, ".Trash-"+strconv.Itoa(uid)))
}

// topdirTrashRoots are all locations that count as trash below topdir for the
// "never trash a trash" refusal, whether or not they pass the checks above.
func topdirTrashRoots(topdir string, uid int) []string {
	return []string{
		filepath.Join(topdir, ".Trash"),
		filepath.Join(topdir, ".Trash-"+strconv.Itoa(uid)),
	}
}

// statOwner returns the uid owning fi. ok is false where the platform
// reports no owner, in which case the ownership check cannot be applied.
func statOwner(fi fs.FileInfo) (uid int, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, false
	}
	return int(st.Uid), true
}

// ensureTrashDir creates dir with its files/ and info/ subdirectories (mode
// 0700). Every level is made with Mkdir and EEXIST is tolerated, never with
// MkdirAll: MkdirAll follows an existing symlink, so a planted files/ link
// would silently redirect the trash. The home trash (strict unset) is the
// user's own tree and only needs its parents created. A strict (topdir) trash
// lives on a mount other users may write to, so each level is verified with
// checkTrashLevel right after it is made and before anything is created
// below it (Mkdir below a planted symlink would write through it); a failure
// makes the caller fall through to the next candidate. Both modes require
// files/ and info/ to end up as real directories, the same condition Restore
// enforces (checkRootOnDisk), so Remove never writes a record that Restore
// would later refuse. The home trash is not checked for ownership or mode, and its root may be
// a symlink to a directory (strict topdir trashes never may).
//
// Known limits. A topdir trash on a filesystem without POSIX permissions
// (vfat, exfat, ntfs) reports mode 0777 and is always rejected by the mode
// check; Remove then falls back to a copy into the home trash, and a
// half-created .Trash-$uid directory may stay behind. The check between
// this function and the later move is inherently racy (TOCTOU); the
// structural checks narrow the window but cannot close it.
func (f *freedesktop) ensureTrashDir(dir string, strict bool) error {
	if !strict {
		if err := ensureHomeRoot(dir); err != nil {
			return err
		}
	}
	for _, d := range []string{dir, filepath.Join(dir, "files"), filepath.Join(dir, "info")} {
		if !strict && d == dir {
			continue // home root handled above; a symlink is allowed there
		}
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("cannot create trash directory %q: %w", d, err)
		}
		if err := f.checkLevel(d, strict); err != nil {
			return err
		}
	}
	return nil
}

// ensureHomeRoot creates the home trash root and its parents. The root may
// legitimately be a symlink (for example to a trash on another disk); it only
// has to resolve to a directory. files/ and info/ below it are still required
// to be real directories by the caller.
func ensureHomeRoot(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create trash directory %q: %w", dir, err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("trash directory %q is not a directory", dir)
	}
	return nil
}

// checkLevel verifies one level made by ensureTrashDir: the full ownership and
// mode check for a strict topdir trash, a real-directory check for the home
// trash.
func (f *freedesktop) checkLevel(d string, strict bool) error {
	if strict {
		return f.checkTrashLevel(d)
	}
	fi, err := f.lstat(d)
	if err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("trash directory %q is not a real directory", d)
	}
	return nil
}

// checkTrashLevel requires d to be a real directory (not a link) that belongs
// to the current user and is closed to group and others (mode&0o077 == 0), as
// GLib demands, so another user can neither have pre-created it nor read what
// is trashed into it. On filesystems that report a single owner (vfat, exfat)
// the owner comparison is a no-op.
func (f *freedesktop) checkTrashLevel(d string) error {
	fi, err := f.lstat(d)
	if err != nil {
		return fmt.Errorf("trash directory %q: %w", d, err)
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("trash directory %q is not a real directory", d)
	}
	if uid, ok := f.ownerOf(fi); ok && uid != f.uid {
		return fmt.Errorf("trash directory %q is owned by uid %d, not %d", d, uid, f.uid)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("trash directory %q is accessible to other users (mode %v)", d, fi.Mode().Perm())
	}
	return nil
}

// checkTrashDir applies checkTrashLevel to dir, dir/files and dir/info.
func (f *freedesktop) checkTrashDir(dir string) error {
	for _, d := range []string{dir, filepath.Join(dir, "files"), filepath.Join(dir, "info")} {
		if err := f.checkTrashLevel(d); err != nil {
			return err
		}
	}
	return nil
}

// isTrashRoot reports whether root has the shape of a trash directory brooom
// may have written to: the home trash, $topdir/.Trash-$uid or
// $topdir/.Trash/$uid, with the uid being the current user's. Restore uses it
// so a hand-edited manifest cannot make brooom move files out of arbitrary
// directories. Whether a topdir root really sits at a mount point and is
// trustworthy is checked separately by checkTopdirRoot.
func (f *freedesktop) isTrashRoot(root string) bool {
	return root == f.homeTrash || f.topdirOf(root) != ""
}

// topdirOf returns the topdir a topdir-shaped trash root belongs to, or "" if
// root is neither $topdir/.Trash-$uid nor $topdir/.Trash/$uid for the current
// uid.
func (f *freedesktop) topdirOf(root string) string {
	uid := strconv.Itoa(f.uid)
	base, parent := filepath.Base(root), filepath.Dir(root)
	switch {
	case base == ".Trash-"+uid:
		return parent
	case base == uid && filepath.Base(parent) == ".Trash":
		return filepath.Dir(parent)
	}
	return ""
}

// checkTopdirRoot verifies that a topdir trash root recorded in a manifest is
// one Remove could have created: its topdir is a mount point (the same
// device-walk Remove uses), no component of the path is a symlink, a shared
// .Trash is a sticky real directory, and the root itself passes
// checkTrashDir. Without it any directory named .Trash-<uid> planted anywhere
// would count as a trash.
func (f *freedesktop) checkTopdirRoot(root, topdir string) error {
	if top, err := findTopdir(topdir, f.deviceOf); err != nil || top != topdir {
		return fmt.Errorf("%q is not the top directory of a mounted filesystem", topdir)
	}
	if real, err := filepath.EvalSymlinks(root); err != nil || real != root {
		return fmt.Errorf("trash directory %q is or passes through a symlink", root)
	}
	if shared := filepath.Dir(root); filepath.Base(shared) == ".Trash" {
		fi, err := f.lstat(shared)
		if err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSticky == 0 {
			return fmt.Errorf("shared trash %q is not a sticky real directory", shared)
		}
	}
	return f.checkTrashDir(root)
}
