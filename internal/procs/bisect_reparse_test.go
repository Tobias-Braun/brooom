package procs

import (
	"io/fs"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/walk"
)

// fakeDirEntry is a directory entry with a chosen mode type.
type fakeDirEntry struct{ mode fs.FileMode }

func (f fakeDirEntry) Name() string               { return "d" }
func (f fakeDirEntry) IsDir() bool                { return f.mode.IsDir() }
func (f fakeDirEntry) Type() fs.FileMode          { return f.mode.Type() }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

func TestIsPlainDirReparsePoints(t *testing.T) {
	tests := []struct {
		name      string
		mode      fs.FileMode
		walkerDir bool
		want      bool
	}{
		{"plain directory", fs.ModeDir, false, true},
		{"symlink directory", fs.ModeDir | fs.ModeSymlink, true, false},
		{"cloud placeholder directory", fs.ModeDir | fs.ModeIrregular, true, true},
		{"junction", fs.ModeDir | fs.ModeIrregular, false, false},
		{"regular file", 0, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := statEntry
			t.Cleanup(func() { statEntry = old })
			statEntry = func(p string) (walk.Entry, error) {
				e := walk.Entry{Path: p}
				if tc.walkerDir {
					e.Type = fs.ModeDir
				}
				return e, nil
			}
			if got := isPlainDir("/x/d", fakeDirEntry{tc.mode}); got != tc.want {
				t.Errorf("isPlainDir = %v, want %v", got, tc.want)
			}
		})
	}
}
