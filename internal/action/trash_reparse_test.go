package action

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/walk"
)

// withRoot presents path to sizeAndNestedVCS as the given entry type, with
// the given answer for "is this a directory on disk", simulating what
// Windows reports for reparse points.
func withRoot(t *testing.T, typ fs.FileMode, dirOnDisk bool) {
	t.Helper()
	oldStat, oldDir := statRoot, lstatIsDir
	statRoot = func(p string) (walk.Entry, error) {
		return walk.Entry{Path: p, Type: typ, Size: 5, Allocated: 5}, nil
	}
	lstatIsDir = func(string) bool { return dirOnDisk }
	t.Cleanup(func() { statRoot, lstatIsDir = oldStat, oldDir })
}

func TestSizeAndNestedGitReparseRoots(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name      string
		typ       fs.FileMode
		dirOnDisk bool
		wantErr   string
		wantSize  int64
	}{
		{"uninspected reparse directory is refused", fs.ModeIrregular, true, "cannot be inspected", 0},
		{"irregular file is sized by its size", fs.ModeIrregular, false, "", 5},
		{"symlink is sized as a link", fs.ModeSymlink, false, "", 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withRoot(t, tc.typ, tc.dirOnDisk)
			m, err := sizeAndNestedVCS(context.Background(), dir)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || m.size != tc.wantSize {
				t.Fatalf("size = %d, err = %v, want %d", m.size, err, tc.wantSize)
			}
		})
	}
}

func TestRefreshFindingRefusesUninspectedReparseDirectory(t *testing.T) {
	dir := t.TempDir()
	withRoot(t, fs.ModeIrregular, true)
	_, err := refreshFinding(context.Background(), trashFinding(dir), dir)
	wantSkip(t, err, "cannot inspect")
}

func TestTreeMeterCountsIrregularFiles(t *testing.T) {
	m := &treeMeter{links: map[string]struct{}{}}
	got := m.entrySize(walk.Entry{Type: fs.ModeIrregular, Size: 11, Allocated: 11})
	if got != 11 {
		t.Errorf("irregular file size = %d, want 11", got)
	}
}

func TestSizeAndNestedGitRealDirectoryStillWalks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := sizeAndNestedVCS(context.Background(), dir)
	if err != nil || m.nestedVCS == "" {
		t.Fatalf("nestedVCS = %q, err = %v, want the nested repository found", m.nestedVCS, err)
	}
}
