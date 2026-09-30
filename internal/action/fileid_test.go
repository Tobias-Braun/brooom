package action

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failingIdentity makes identityOf fail with err for every path ending in
// suffix, standing in for a Windows delete-pending or ACL-denied entry whose
// file ID cannot be read.
func failingIdentity(t *testing.T, suffix string, err error) {
	t.Helper()
	old := identityOf
	identityOf = func(p string, follow bool) (fileID, error) {
		if strings.HasSuffix(filepath.ToSlash(p), suffix) {
			return nil, &fs.PathError{Op: "identity", Path: p, Err: err}
		}
		return old(p, follow)
	}
	t.Cleanup(func() { identityOf = old })
}

// TestIsSameEntryFailsClosedOnIdentityErrors: an identity that cannot be read
// for an existing entry counts as "same"; only an absent entry is different.
func TestIsSameEntryFailsClosedOnIdentityErrors(t *testing.T) {
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
		{"sharing violation", errors.New("used by another process"), true},
		{"not exist", fs.ErrNotExist, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failingIdentity(t, "/b", tc.err)
			if got := isSameEntry(a, filepath.Join(dir, "b")); got != tc.want {
				t.Fatalf("isSameEntry = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRefuseRestoreFailsClosedOnUnreadableIdentity: a restore destination
// below an entry whose identity cannot be read is refused, not waved through
// as "not the same".
func TestRefuseRestoreFailsClosedOnUnreadableIdentity(t *testing.T) {
	fx := newTrashFixture(t)
	fx.mkdir("proj/.git")
	dest := fx.mkdir("proj/src")
	failingIdentity(t, "/proj/src", fs.ErrPermission)
	if err := refuseRestoreTarget(filepath.Join(dest, "a.txt")); err == nil {
		t.Fatal("restore under an unreadable-identity entry was allowed")
	}
}

// TestRefuseRestoreAllowsReadableIdentity guards against the fail-closed path
// refusing everything: a normal destination still passes.
func TestRefuseRestoreAllowsReadableIdentity(t *testing.T) {
	fx := newTrashFixture(t)
	dest := fx.mkdir("proj/src")
	if err := refuseRestoreTarget(filepath.Join(dest, "a.txt")); err != nil {
		t.Fatalf("ordinary destination refused: %v", err)
	}
}
