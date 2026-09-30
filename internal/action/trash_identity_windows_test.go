//go:build windows

package action

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// shortPath returns the 8.3 alias of an existing path, skipping the test when
// the volume has short name generation disabled.
func shortPath(t *testing.T, p string) string {
	t.Helper()
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("no 8.3 name available: %v", err)
	}
	return windows.UTF16ToString(buf[:n])
}

func TestTrashRefusesRealShortNameOfGitDir(t *testing.T) {
	fx := newTrashFixture(t)
	git := fx.mkdir("repo/.git")
	if err := os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	short := shortPath(t, git)
	if filepath.Base(short) == ".git" {
		t.Skip("volume does not generate 8.3 names")
	}
	_, err := trashAction{}.Plan(t.Context(), fx.env, trashFinding(short))
	wantSkip(t, err, ".git")
	if _, statErr := os.Stat(filepath.Join(git, "HEAD")); statErr != nil {
		t.Fatalf(".git was touched: %v", statErr)
	}
}
