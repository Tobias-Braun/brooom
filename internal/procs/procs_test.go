package procs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// resolvedTemp returns a symlink-resolved temp dir: on macOS t.TempDir lives
// below /var, a symlink to /private/var, and lsof reports the real path.
func resolvedTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// query runs OpenFiles and skips the test when the mechanism is unavailable
// on this machine (no lsof, restricted /proc, Restart Manager failing).
func query(t *testing.T, paths ...string) map[string]bool {
	t.Helper()
	// The production default budget is tuned for interactive use; a loaded CI
	// runner (macOS under -race) can need longer for a machine-wide lsof.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := OpenFiles(ctx, paths)
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("open-file detection unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("OpenFiles: %v", err)
	}
	return res
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFilesFileLifecycle(t *testing.T) {
	dir := resolvedTemp(t)
	open := filepath.Join(dir, "open.log")
	closed := filepath.Join(dir, "closed.log")
	writeFile(t, open)
	writeFile(t, closed)

	f, err := os.Open(open)
	if err != nil {
		t.Fatal(err)
	}
	res := query(t, open, closed)
	if !res[open] {
		t.Errorf("open file reported as not open: %v", res)
	}
	if v, ok := res[closed]; !ok || v {
		t.Errorf("sibling file: got %v (present %v), want false", v, ok)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if res := query(t, open); res[open] {
		t.Error("closed file still reported as open")
	}
}

func TestOpenFilesDirectories(t *testing.T) {
	root := resolvedTemp(t)
	busy := filepath.Join(root, "busy")
	idle := filepath.Join(root, "idle")
	// A sibling whose name starts with the same characters must not match by
	// plain string prefix.
	lookalike := filepath.Join(root, "busy-other")
	for _, d := range []string{filepath.Join(busy, "sub"), idle, lookalike} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(busy, "sub", "app.log")
	writeFile(t, file)
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	res := query(t, busy, idle, lookalike)
	want := map[string]bool{busy: true, idle: false, lookalike: false}
	for p, w := range want {
		if res[p] != w {
			t.Errorf("%s: got %v, want %v", p, res[p], w)
		}
	}
}

func TestOpenFilesOddNames(t *testing.T) {
	dir := resolvedTemp(t)
	names := []string{"with space.log", "  leading and trailing  .log"}
	if runtime.GOOS != "windows" {
		names = append(names, "new\nline.log")
	}
	var paths []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		writeFile(t, p)
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		paths = append(paths, p)
	}
	res := query(t, paths...)
	for _, p := range paths {
		if !res[p] {
			t.Errorf("%q not reported as open", p)
		}
	}
}

func TestOpenFilesMissingPathIsFalse(t *testing.T) {
	missing := filepath.Join(resolvedTemp(t), "nope.log")
	res, err := OpenFiles(context.Background(), []string{missing})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := res[missing]; !ok || v {
		t.Errorf("got %v (present %v), want false", v, ok)
	}
}

func TestOpenFilesNormalizesInput(t *testing.T) {
	dir := resolvedTemp(t)
	file := filepath.Join(dir, "a.log")
	writeFile(t, file)
	messy := dir + string(filepath.Separator) + "." + string(filepath.Separator) + "a.log"

	res, err := OpenFiles(context.Background(), []string{"", messy, file, ""})
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("want one deduplicated key, got %v", res)
	}
	if _, ok := res[file]; !ok {
		t.Errorf("cleaned path missing from result: %v", res)
	}
}

func TestOpenFilesRejectsRelativePath(t *testing.T) {
	res, err := OpenFiles(context.Background(), []string{filepath.Join(resolvedTemp(t), "ok"), filepath.Join("rel", "x.log")})
	if err == nil {
		t.Fatal("expected an error for a relative path")
	}
	if res != nil {
		t.Errorf("no result expected on invalid input, got %v", res)
	}
}

func TestOpenFilesNoPaths(t *testing.T) {
	res, err := OpenFiles(context.Background(), nil)
	if err != nil || len(res) != 0 {
		t.Errorf("got %v, %v", res, err)
	}
}

func TestOpenFilesSymlinkIsNotFollowed(t *testing.T) {
	dir := resolvedTemp(t)
	target := filepath.Join(dir, "target.log")
	link := filepath.Join(dir, "link.log")
	writeFile(t, target)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	f, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// The link itself is not held open; only the resolved target is. Windows
	// Restart Manager resolves the link to its target, so the link's result
	// is unspecified there (documented on OpenFiles).
	res := query(t, link, target)
	if runtime.GOOS != "windows" && res[link] {
		t.Error("symlink must not be resolved to its target")
	}
	if !res[target] {
		t.Error("target should be open")
	}
}

func TestOpenFilesCancelledContext(t *testing.T) {
	file := filepath.Join(resolvedTemp(t), "a.log")
	writeFile(t, file)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	res, err := OpenFiles(ctx, []string{file})
	if time.Since(start) > time.Second {
		t.Errorf("cancelled call took %v", time.Since(start))
	}
	if !errors.Is(err, ErrIncomplete) && !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v, want ErrIncomplete or ErrUnavailable", err)
	}
	if _, ok := res[file]; !ok {
		t.Error("partial result must still contain every valid path")
	}
}

func TestDirPrefix(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.VolumeName(os.TempDir()) + sep
	tests := []struct{ in, want string }{
		{root, root},
		{root + "a", root + "a" + sep},
		{root + "a" + sep, root + "a" + sep},
	}
	for _, tt := range tests {
		if got := dirPrefix(tt.in); got != tt.want {
			t.Errorf("dirPrefix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
