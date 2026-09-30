package walk

import (
	"io/fs"
	"testing"
	"time"
)

// fakeInfo is a fs.FileInfo with a chosen mode, standing in for what Go 1.23+
// reports on Windows for reparse points that a Linux test cannot create.
type fakeInfo struct {
	name string
	mode fs.FileMode
	size int64
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Unix(1_700_000_000, 0) }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

// withSurrogate replaces the reparse tag lookup for one test.
func withSurrogate(t *testing.T, fn func(string) bool) {
	t.Helper()
	old := nameSurrogate
	nameSurrogate = fn
	t.Cleanup(func() { nameSurrogate = old })
}

func TestEntryFromInfoReparseDirectories(t *testing.T) {
	tests := []struct {
		name      string
		mode      fs.FileMode
		surrogate bool
		wantDir   bool
		wantSize  int64
	}{
		{"cloud placeholder dir stays a directory", fs.ModeDir | fs.ModeIrregular, false, true, 0},
		{"junction dir is not descended", fs.ModeDir | fs.ModeIrregular, true, false, 7},
		{"symlink dir is not descended", fs.ModeDir | fs.ModeSymlink, false, false, 7},
		{"plain dir", fs.ModeDir, false, true, 0},
		{"cloud file keeps its size", fs.ModeIrregular, false, false, 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withSurrogate(t, func(string) bool { return tc.surrogate })
			e := entryFromInfo("/x/y", fakeInfo{name: "y", mode: tc.mode, size: 7})
			if e.IsDir() != tc.wantDir {
				t.Errorf("IsDir = %v, want %v (type %v)", e.IsDir(), tc.wantDir, e.Type)
			}
			if e.Size != tc.wantSize {
				t.Errorf("Size = %d, want %d", e.Size, tc.wantSize)
			}
		})
	}
}

func TestIrregularFilesAreSized(t *testing.T) {
	e := Entry{Name: "cloud.bin", Type: fs.ModeIrregular, Size: 9, Allocated: 9}
	if !e.IsSizedFile() {
		t.Fatal("irregular non-directory entry must count by its size")
	}
	r := &dirRecord{}
	r.add(e)
	if r.DirectBytes != 9 || r.DirectFiles != 1 {
		t.Errorf("record = %d bytes, %d files, want 9 and 1", r.DirectBytes, r.DirectFiles)
	}
	if (Entry{Type: fs.ModeSocket}).IsSizedFile() {
		t.Error("sockets carry no data and must not be sized")
	}
}
