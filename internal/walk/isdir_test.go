package walk

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestWalkSkipsAllVCSMetadata: the walker lists a VCS metadata directory but
// never descends into it, whichever system it belongs to.
func TestWalkSkipsAllVCSMetadata(t *testing.T) {
	root := t.TempDir()
	for _, meta := range []string{".git", ".hg", ".jj", ".svn"} {
		writeFile(t, filepath.Join(root, meta, "inner", "f"), 3)
	}
	writeFile(t, filepath.Join(root, "src", "a"), 3)
	c := newCollected()
	if err := Walk(context.Background(), root, Options{}, c.visit, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range c.rels() {
		if strings.Contains(r, "inner") {
			t.Errorf("walk descended into VCS metadata: %s", r)
		}
	}
}

// fakeDirEntry adapts fakeInfo to fs.DirEntry.
type fakeDirEntry struct{ fakeInfo }

func (f fakeDirEntry) Type() fs.FileMode          { return f.mode.Type() }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return f.fakeInfo, nil }

// TestIsDirEntryReparse: a junction (name surrogate) listed by ReadDir is not
// a directory, a cloud placeholder is, so detectors agree with walk and trash.
func TestIsDirEntryReparse(t *testing.T) {
	tests := []struct {
		name      string
		mode      fs.FileMode
		surrogate bool
		want      bool
	}{
		{"junction", fs.ModeDir | fs.ModeIrregular, true, false},
		{"cloud placeholder", fs.ModeDir | fs.ModeIrregular, false, true},
		{"symlink", fs.ModeSymlink, false, false},
		{"plain dir", fs.ModeDir, false, true},
		{"file", 0, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withSurrogate(t, func(string) bool { return tc.surrogate })
			if got := IsDirEntry("/x", fakeDirEntry{fakeInfo{name: "y", mode: tc.mode}}); got != tc.want {
				t.Fatalf("IsDirEntry = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIsDirNoFollow covers the Lstat based helper on real entries.
func TestIsDirNoFollow(t *testing.T) {
	root := sampleTree(t)
	link := filepath.Join(t.TempDir(), "link")
	mkSymlink(t, root, link)
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"directory", root, true},
		{"symlink to directory", link, false},
		{"missing", filepath.Join(root, "nope"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDirNoFollow(tc.path); got != tc.want {
				t.Fatalf("IsDirNoFollow = %v, want %v", got, tc.want)
			}
		})
	}
}
