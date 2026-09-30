package trash

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/walk"
)

// fiveByteFileSize is the size the trashers must report for the 5 byte files
// the tests create: their allocation (a whole block on most filesystems), not
// their 5 logical bytes.
func fiveByteFileSize(t testing.TB) int64 {
	t.Helper()
	p := filepath.Join(t.TempDir(), "five")
	if err := os.WriteFile(p, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	return refSize(t, p)
}

// refSize is an independent reference for the sizing rule the trashers must
// follow: allocated bytes of files and directories (directory blocks
// included), a symlink as its own length, nothing followed. It is computed
// with filepath.WalkDir, not with walk.DirSize, and must be called before
// the item is removed. The tests use trees without hard links.
func refSize(t testing.TB, path string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			total += fi.Size()
		case fi.IsDir(), fi.Mode().IsRegular():
			total += walk.AllocatedSize(fi)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}
