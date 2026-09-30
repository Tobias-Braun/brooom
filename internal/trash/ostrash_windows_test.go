//go:build windows

package trash

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// These tests call the real Recycle Bin and therefore run on Windows CI only.
// Every bin item they create is removed again by cleanupBin, which deletes
// exactly the $I/$R pair of the record and never touches other bin content.

func newTestTrasher(t *testing.T) *winTrash {
	t.Helper()
	tr, err := newOSTrasher(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return tr.(*winTrash)
}

// cleanupBin removes the bin pair of rec after the test.
func cleanupBin(t *testing.T, rec Record) {
	t.Helper()
	t.Cleanup(func() {
		if rec.StoredPath != "" {
			_ = os.RemoveAll(rec.StoredPath)
		}
		if rec.InfoPath != "" {
			_ = os.Remove(rec.InfoPath)
		}
	})
}

// remove trashes path and skips the test when the machine's bin cannot take
// it by design (for example a CI image without bin settings in the registry).
func remove(t *testing.T, tr *winTrash, path string) Record {
	t.Helper()
	rec, err := tr.Remove(context.Background(), path)
	if err != nil {
		if strings.Contains(err.Error(), "--trash-strategy quarantine") {
			t.Skipf("Recycle Bin unavailable on this machine: %v", err)
		}
		t.Fatalf("Remove(%q): %v", path, err)
	}
	cleanupBin(t, rec)
	return rec
}

func TestShellStructLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("layout is only defined for 64-bit Windows")
	}
	var op shFileOpStruct
	if got := unsafe.Sizeof(op); got != 56 {
		t.Errorf("sizeof = %d, want 56", got)
	}
	if got := unsafe.Offsetof(op.fFlags); got != 32 {
		t.Errorf("fFlags offset = %d, want 32", got)
	}
	if got := unsafe.Offsetof(op.fAnyOperationsAborted); got != 36 {
		t.Errorf("fAnyOperationsAborted offset = %d, want 36", got)
	}
}

func TestRemoveRestoreFile(t *testing.T) {
	tr := newTestTrasher(t)
	names := []string{"plain.txt", "with space.txt", "café ünï.txt", "brack[et] (1).txt", "日本語.txt"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
				t.Fatal(err)
			}
			rec := remove(t, tr, p)
			checkFileRecord(t, rec, p)
			if err := tr.Restore(context.Background(), rec); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			checkRestoredFile(t, rec, p)
		})
	}
}

// checkRestoredFile asserts the content is back and the $I file is gone.
func checkRestoredFile(t *testing.T, rec Record, p string) {
	t.Helper()
	if b, err := os.ReadFile(p); err != nil || string(b) != "hello" {
		t.Fatalf("restored content = %q, %v", b, err)
	}
	if _, err := os.Lstat(rec.InfoPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("$I file remains: %v", err)
	}
}

// checkFileRecord asserts the record of a trashed 5-byte file.
func checkFileRecord(t *testing.T, rec Record, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
	if !rec.Restorable || rec.StoredPath == "" || rec.InfoPath == "" || rec.SizeBytes != 5 || rec.IsDir {
		t.Fatalf("bad record: %+v", rec)
	}
	if rec.Strategy != config.StrategyTrash {
		t.Errorf("strategy = %q", rec.Strategy)
	}
}

func TestRemoveRestoreDir(t *testing.T) {
	tr := newTestTrasher(t)
	dir := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.txt", filepath.Join("sub", "b.txt")} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("123"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := remove(t, tr, dir)
	if !rec.IsDir || rec.SizeBytes != 6 || !rec.Restorable {
		t.Fatalf("bad record: %+v", rec)
	}
	if err := tr.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "b.txt")); err != nil {
		t.Errorf("directory not restored whole: %v", err)
	}
}

func TestRemoveSymlinkKeepsTarget(t *testing.T) {
	tr := newTestTrasher(t)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	rec := remove(t, tr, link)
	if rec.IsDir {
		t.Error("a link must not be recorded as a directory")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("link target was affected: %v", err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("link still exists: %v", err)
	}
	if err := tr.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("link not restored as a link: %v", err)
	}
}

func TestRemoveLockedFile(t *testing.T) {
	tr := newTestTrasher(t)
	p := filepath.Join(t.TempDir(), "locked.txt")
	// Opening with syscall.CreateFile and no FILE_SHARE_DELETE is what
	// os.OpenFile does: the shell cannot move a file opened this way.
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = tr.Remove(context.Background(), p)
	if err == nil {
		t.Fatal("locked file was removed")
	}
	if strings.Contains(err.Error(), "--trash-strategy quarantine") {
		t.Skipf("Recycle Bin unavailable on this machine: %v", err)
	}
	if !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "in use") {
		t.Errorf("error does not name the file and the lock: %v", err)
	}
	if !errors.Is(err, syscall.Errno(32)) {
		t.Logf("error does not wrap ERROR_SHARING_VIOLATION (abort path): %v", err)
	}
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("locked file is not intact: %v", err)
	}
}

func TestRecycleBinRefusals(t *testing.T) {
	tr := newTestTrasher(t)
	drive := filepath.VolumeName(os.TempDir()) + `\`
	tests := map[string]string{
		"empty":      "",
		"relative":   `some\file.txt`,
		"drive root": drive,
		"unc root":   `\\server\share`,
		"wildcard":   filepath.Join(t.TempDir(), "*.txt"),
		"in bin":     filepath.Join(drive, binDirName, "S-1-5-21-1", "$RABC"),
	}
	for name, p := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := tr.Remove(context.Background(), p); err == nil {
				t.Errorf("Remove(%q) succeeded", p)
			}
		})
	}
}

func TestRemoveNonexistent(t *testing.T) {
	tr := newTestTrasher(t)
	_, err := tr.Remove(context.Background(), filepath.Join(t.TempDir(), "nope.txt"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

// fakeSettings returns fixed settings for the injected reader.
type fakeSettings struct {
	s   binSettings
	err error
}

func (f fakeSettings) read(string) (binSettings, error) { return f.s, f.err }

func TestRemovePreflightRefuses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := map[string]fakeSettings{
		"nuke":       {s: binSettings{NukeOnDelete: true, MaxCapacityMB: 1000}},
		"too big":    {s: binSettings{MaxCapacityMB: 0}},
		"unreadable": {err: errors.New("no key")},
	}
	for name, fs := range tests {
		t.Run(name, func(t *testing.T) {
			tr := newTestTrasher(t)
			tr.settings = fs
			if _, err := tr.Remove(context.Background(), p); err == nil {
				t.Fatal("Remove succeeded")
			}
			if _, err := os.Lstat(p); err != nil {
				t.Errorf("file touched despite refusal: %v", err)
			}
		})
	}
}

func TestRestoreRefusals(t *testing.T) {
	tr := newTestTrasher(t)
	orig := filepath.Join(t.TempDir(), "f.txt")
	outside := filepath.Join(t.TempDir(), "$Rfake")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		rec  Record
		want error
	}{
		{"wrong strategy", Record{Strategy: config.StrategyQuarantine, OriginalPath: orig}, ErrNotRestorable},
		{"missing stored", Record{Strategy: config.StrategyTrash, OriginalPath: orig,
			StoredPath: filepath.VolumeName(orig) + `\$Recycle.Bin\S-1-5-21-0\$RNOPE.txt`}, ErrNotRestorable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tr.Restore(context.Background(), tt.rec); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
	t.Run("stored outside bin", func(t *testing.T) {
		rec := Record{Strategy: config.StrategyTrash, OriginalPath: orig, StoredPath: outside, Restorable: true}
		if err := tr.Restore(context.Background(), rec); err == nil {
			t.Fatal("restore from outside the bin accepted")
		}
		if _, err := os.Lstat(outside); err != nil {
			t.Errorf("file outside the bin was moved: %v", err)
		}
	})
}

func TestRestoreConflictAndSearch(t *testing.T) {
	tr := newTestTrasher(t)
	p := filepath.Join(t.TempDir(), "again.txt")
	if err := os.WriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := remove(t, tr, p)
	if err := os.WriteFile(p, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.Restore(context.Background(), rec); !errors.Is(err, ErrRestoreConflict) {
		t.Fatalf("err = %v, want ErrRestoreConflict", err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	// A record without StoredPath is found again by path and time.
	searched := Record{Strategy: config.StrategyTrash, OriginalPath: p, RemovedAt: rec.RemovedAt.Add(time.Second)}
	if err := tr.Restore(context.Background(), searched); err != nil {
		t.Fatalf("Restore by search: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one" {
		t.Errorf("content = %q", b)
	}
}
