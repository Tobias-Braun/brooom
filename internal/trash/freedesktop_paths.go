//go:build unix && !darwin

package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	return "", errors.New("cannot locate the trash: neither XDG_DATA_HOME nor HOME is set to an absolute path; use --trash-strategy quarantine")
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

// ensureTrashDir creates dir with its files/ and info/ subdirectories (mode
// 0700). With strict set, dir must end up a real directory and not a symlink,
// which is required for topdir trashes: another user could have planted a
// link there.
func ensureTrashDir(dir string, strict bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create trash directory %q: %w", dir, err)
	}
	if strict {
		fi, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("trash directory %q is not a real directory", dir)
		}
	}
	for _, sub := range []string{"files", "info"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return fmt.Errorf("cannot create trash directory %q: %w", filepath.Join(dir, sub), err)
		}
	}
	return nil
}

var (
	topdirRootRe = regexp.MustCompile(`^\.Trash-[0-9]+$`)
	uidDirRe     = regexp.MustCompile(`^[0-9]+$`)
)

// isTrashRoot reports whether root has the shape of a trash directory brooom
// may have written to: the home trash, $topdir/.Trash-$uid or
// $topdir/.Trash/$uid. Restore uses it so a hand-edited manifest cannot make
// brooom move files out of arbitrary directories.
func (f *freedesktop) isTrashRoot(root string) bool {
	if root == f.homeTrash {
		return true
	}
	base := filepath.Base(root)
	if topdirRootRe.MatchString(base) {
		return true
	}
	return uidDirRe.MatchString(base) && filepath.Base(filepath.Dir(root)) == ".Trash"
}
