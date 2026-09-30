package trash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// ErrNotInOSTrash is returned when a stored path lies outside every
// directory an OS trash uses, so emptying it could delete something else.
var ErrNotInOSTrash = errors.New("not inside an OS trash directory")

// ErrStoredChanged is returned when the stored copy is no longer the item
// Brooom moved to the trash (another type, or a file of another size).
var ErrStoredChanged = errors.New("the item in the trash changed since brooom moved it there")

// InOSTrash reports whether path lies strictly inside a directory an OS trash
// uses on any platform: a ".Trash" or ".Trashes" folder (macOS), the "files"
// or "info" folder of a freedesktop trash ("Trash", ".Trash-<uid>",
// ".Trash/<uid>") or a "$Recycle.Bin" (Windows). The check is by path components, independent
// of the host, because the recorded path is what is judged, and it never
// accepts the trash directory itself.
func InOSTrash(path string) bool {
	parts := strings.FieldsFunc(filepath.ToSlash(filepath.Clean(path)), func(r rune) bool { return r == '/' || r == '\\' })
	for i := range parts[:max(len(parts)-1, 0)] {
		name := parts[i]
		switch {
		case strings.EqualFold(name, "$Recycle.Bin"), isTrashDirName(name) && !isFreedesktopFiles(parts, i):
			return true
		case (name == "files" || name == "info") && i > 0 && isFreedesktopTrash(parts, i-1):
			return true
		}
	}
	return false
}

// isFreedesktopTrash reports whether parts[i] is the top of a freedesktop
// trash: "Trash" (the home trash below ~/.local/share), ".Trash-<uid>" or
// the "<uid>" folder below a shared ".Trash".
func isFreedesktopTrash(parts []string, i int) bool {
	name := parts[i]
	switch {
	case name == "Trash", strings.HasPrefix(name, ".Trash-"):
		return true
	case i > 0 && parts[i-1] == ".Trash" && name != "" && strings.Trim(name, "0123456789") == "":
		return true
	}
	return false
}

// isFreedesktopFiles reports whether the ".Trash" at parts[i] is the shared
// freedesktop trash, whose items live further down in <uid>/files; that case
// is judged by the "files" rule instead.
func isFreedesktopFiles(parts []string, i int) bool {
	return i+2 < len(parts) && strings.Trim(parts[i+1], "0123456789") == "" && (parts[i+2] == "files" || parts[i+2] == "info")
}

// VerifyStored checks that the stored copy of an OS trash record is still
// what Brooom put there: inside an OS trash directory, of the recorded type,
// and for a file of the recorded size (allocated bytes, the sizing rule of
// the whole program). Directories are compared by type only: a cross-device
// move can change how their files are allocated.
func VerifyStored(r Record) error {
	if r.Strategy != config.StrategyTrash || r.StoredPath == "" {
		return fmt.Errorf("%w: not an OS trash record", ErrNotInOSTrash)
	}
	if !InOSTrash(r.StoredPath) {
		return fmt.Errorf("%s: %w", r.StoredPath, ErrNotInOSTrash)
	}
	fi, err := os.Lstat(r.StoredPath)
	if err != nil {
		return err
	}
	if !sameIdentity(fi, r) {
		return fmt.Errorf("%s: %w", r.StoredPath, ErrStoredChanged)
	}
	return nil
}

// sameIdentity compares the type, and for regular files the size, with the
// record.
func sameIdentity(fi os.FileInfo, r Record) bool {
	isDir := fi.IsDir() && fi.Mode()&os.ModeSymlink == 0
	if isDir != r.IsDir {
		return false
	}
	return isDir || !fi.Mode().IsRegular() || r.SizeBytes <= 0 || walk.AllocatedSize(fi) == r.SizeBytes
}

// RemoveStored permanently deletes the stored copy of a verified OS trash
// record and its metadata file (freedesktop .trashinfo, Windows $I), so the
// trash does not keep a dangling entry. Symlinks are removed as links.
func RemoveStored(r Record) error {
	if err := VerifyStored(r); err != nil {
		return err
	}
	if err := os.RemoveAll(r.StoredPath); err != nil {
		return err
	}
	if r.InfoPath != "" && InOSTrash(r.InfoPath) {
		if err := os.Remove(r.InfoPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("the item is gone, but its trash metadata %s stays: %w", r.InfoPath, err)
		}
	}
	return nil
}
