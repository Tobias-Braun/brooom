package action

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// failingLstat makes lstat fail with err for every path ending in suffix and
// restores the real call afterwards.
func failingLstat(t *testing.T, suffix string, err error) {
	t.Helper()
	old := lstat
	lstat = func(p string) (os.FileInfo, error) {
		if strings.HasSuffix(filepath.ToSlash(p), suffix) {
			return nil, &fs.PathError{Op: "lstat", Path: p, Err: err}
		}
		return old(p)
	}
	t.Cleanup(func() { lstat = old })
}

// TestIsSameEntryFailsClosedOnStatErrors: an entry that exists but cannot be
// inspected (access denied) has an unknown identity, which must count as a
// match so the removal is refused. A vanished entry cannot alias anything.
func TestIsSameEntryFailsClosedOnStatErrors(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"access denied", fs.ErrPermission, true},
		{"io error", syscall.EIO, true},
		{"not exist", fs.ErrNotExist, false},
		{"parent is a file", syscall.ENOTDIR, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failingLstat(t, "/b", tc.err)
			if got := isSameEntry(a, filepath.Join(dir, "b")); got != tc.want {
				t.Fatalf("isSameEntry = %v, want %v", got, tc.want)
			}
			if got := isSameEntry(filepath.Join(dir, "b"), a); got != tc.want {
				t.Fatalf("isSameEntry (swapped) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRefuseByIdentityRefusesWhenGitEntryUnreadable: a protected .git next to
// the target that cannot be stat'ed must block the removal.
func TestRefuseByIdentityRefusesWhenGitEntryUnreadable(t *testing.T) {
	fx := newTrashFixture(t)
	p := fx.mkdir("proj/node_modules")
	fx.mkdir("proj/.git")
	if err := RefuseByIdentity(p); err != nil {
		t.Fatalf("baseline refused: %v", err)
	}
	failingLstat(t, "proj/.git", fs.ErrPermission)
	wantSkip(t, RefuseByIdentity(p), ".git")
}
