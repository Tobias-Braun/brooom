package scope

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// requireSymlink skips the test when the process may not create symlinks.
// Windows needs a privilege (or developer mode) for that, so a permission
// error there is an environment limit, not a failure; on other platforms it
// is a real error.
func requireSymlink(t testing.TB) {
	t.Helper()
	dir := testutil.ResolvedTempDir(t)
	err := os.Symlink(dir, filepath.Join(dir, "probe"))
	if err == nil {
		return
	}
	if runtime.GOOS == "windows" && (errors.Is(err, fs.ErrPermission) || os.IsPermission(err)) {
		t.Skip("symlinks need privilege on Windows: " + err.Error())
	}
	t.Fatalf("cannot create symlinks: %v", err)
}

// mkdir creates a directory tree and returns its path.
func mkdir(t testing.TB, parts ...string) string {
	t.Helper()
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// touch creates an empty file, creating parent directories.
func touch(t testing.TB, parts ...string) string {
	t.Helper()
	path := filepath.Join(parts...)
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// symlink creates link -> target; target is stored verbatim so relative
// targets stay relative.
func symlink(t testing.TB, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// tree is the fixed layout most guard tests run on:
//
//	root/allowed/in/sub/file     the allowed location with content
//	root/allowedc, allowed.evil  siblings whose names share the prefix
//	root/outside/secret          a directory that must stay unreachable
type tree struct {
	root, allowed, in, outside string
	guard                      *Guard
}

// newTree builds the layout and a guard allowing root/allowed.
func newTree(t testing.TB) *tree {
	t.Helper()
	root := testutil.ResolvedTempDir(t)
	tr := &tree{
		root:    root,
		allowed: mkdir(t, root, "allowed"),
		outside: mkdir(t, root, "outside"),
	}
	tr.in = mkdir(t, tr.allowed, "in", "sub")
	tr.in = filepath.Dir(tr.in)
	touch(t, tr.in, "sub", "file")
	touch(t, tr.outside, "secret")
	mkdir(t, root, "allowedc")
	mkdir(t, root, "allowed.evil")
	g, err := NewGuard(tr.allowed)
	if err != nil {
		t.Fatal(err)
	}
	tr.guard = g
	return tr
}

// volumeRoot returns the root of the volume the temp dir lives on.
func volumeRoot(t testing.TB) string {
	t.Helper()
	dir := testutil.ResolvedTempDir(t)
	return filepath.VolumeName(dir) + string(os.PathSeparator)
}
