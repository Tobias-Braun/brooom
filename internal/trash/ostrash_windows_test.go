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

	"golang.org/x/sys/windows/registry"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// These tests call the real Recycle Bin and therefore run on Windows CI only.
// Every bin item they create is removed again by cleanupBin, which deletes
// exactly the $I/$R pair of the record and never touches other bin content.

// newTestTrasher returns a winTrash that calls the real shell but reads
// injected bin settings (bin enabled, huge limit). A fresh CI image has no
// BitBucket registry key, and the real reader would then make the pre-flight
// refuse every item, so no real-shell behaviour would ever be exercised.
func newTestTrasher(t *testing.T) *winTrash {
	t.Helper()
	tr, err := newOSTrasher(Options{})
	if err != nil {
		t.Fatal(err)
	}
	w := tr.(*winTrash)
	w.settings = fakeSettings{s: binSettings{NukeOnDelete: false, MaxCapacityMB: 1 << 20}}
	return w
}

// failOnHint fails the test when err is a pre-flight refusal. With injected
// settings such a refusal is a regression (for example a volume without GUID
// or a broken path check) and must never be hidden behind a skip.
func failOnHint(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), quarantineHint) {
		t.Fatalf("unexpected pre-flight refusal: %v", err)
	}
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

// remove trashes path and fails the test on any error.
func remove(t *testing.T, tr *winTrash, path string) Record {
	t.Helper()
	rec, err := tr.Remove(context.Background(), path)
	if err != nil {
		logBinEntries(t, path)
		t.Fatalf("Remove(%q): %v", path, err)
	}
	cleanupBin(t, rec)
	return rec
}

// logBinEntries logs what the bin holds for the volume of path, so a failed
// lookup on CI shows which spelling and time the shell recorded.
func logBinEntries(t *testing.T, path string) {
	t.Helper()
	_, entries, err := listBin(path, nil)
	t.Logf("bin entries (err=%v), long path %q:", err, longPath(path))
	for _, e := range entries {
		t.Logf("  %s path=%q deleted=%v", e.Name, e.Info.Path, e.Info.DeletedAt)
	}
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
	failOnHint(t, err)
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
	sid, err := currentSID()
	if err != nil {
		t.Fatal(err)
	}
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
			StoredPath: filepath.VolumeName(orig) + `\$Recycle.Bin\` + sid + `\$RNOPE.txt`}, ErrNotRestorable},
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
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("conflicting file was touched, content = %q", b)
	}
	if _, err := os.Lstat(rec.StoredPath); err != nil {
		t.Fatalf("bin item was moved despite the conflict: %v", err)
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

// TestRealRegistrySettings uses the machine's real registry settings, as the
// shipped binary does. It may skip: a fresh image has no BitBucket key until
// the Recycle Bin properties were opened once, and Remove then refuses on
// purpose. Any other outcome must be a working removal.
func TestRealRegistrySettings(t *testing.T) {
	tr, err := newOSTrasher(Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "real.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := tr.Remove(context.Background(), p)
	if err != nil {
		if strings.Contains(err.Error(), quarantineHint) {
			t.Skipf("Recycle Bin settings unavailable on this machine: %v", err)
		}
		t.Fatal(err)
	}
	cleanupBin(t, rec)
	checkFileRecord(t, rec, p)
}

// TestNukeSituationKeepsItem puts the shell into a nuke situation on purpose:
// the bin limit of the test volume is lowered below the item size and the
// shell is called directly, bypassing the pre-flight. Observed on Windows CI:
// FOF_WANTNUKEWARNING makes the shell wait for a confirmation dialog that
// FOF_SILENT does not suppress, so the call blocks (this is why the pre-flight
// exists and why the call here is bounded). The test pins that behaviour: the
// call either blocks with the item untouched, or, should a Windows version
// behave differently, returns with the item intact or in the bin. It fails
// when the item is gone from both its place and the bin. It changes the
// current user's registry, so it runs on CI only, and it restores the value.
func TestNukeSituationKeepsItem(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("modifies the Recycle Bin registry settings, runs on CI only")
	}
	// Not t.TempDir: a call blocked in the shell may still hold the item, and
	// a failing directory removal must not fail the test.
	dir, err := os.MkdirTemp("", "brooom-nuke")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(p, make([]byte, 3<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	guid, err := volumeGUID(p)
	if err != nil {
		t.Skipf("volume has no GUID: %v", err)
	}
	setBinCapacity(t, guid, 1)

	from, err := buildFromBuffer([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	code, aborted, blocked := shellDeleteWithTimeout(from, nukeCallTimeout)
	if blocked {
		t.Logf("observed: the shell blocks on the nuke dialog (no return within %v)", nukeCallTimeout)
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("the shell is blocked but the item is already gone: %v", err)
		}
		return
	}
	t.Logf("shell result: code=0x%X aborted=%v", code, aborted)

	if _, statErr := os.Lstat(p); statErr == nil {
		t.Logf("observed: the call returned and the item was left intact")
		return
	}
	binDir, entries, err := listBin(p, nil)
	if err != nil {
		t.Fatalf("item is gone and the bin cannot be read: %v", err)
	}
	m, ok := chooseRecycledAny(entries, []string{longPath(p), p}, at, matchTolerance)
	if !ok {
		t.Fatalf("FOF_WANTNUKEWARNING did not protect the item: it was deleted permanently (code=0x%X aborted=%v)", code, aborted)
	}
	cleanupBin(t, Record{StoredPath: filepath.Join(binDir, storedName(m.Name)), InfoPath: filepath.Join(binDir, m.Name)})
	t.Logf("observed: the call returned and the item was moved to the bin in spite of the lowered limit")
}

// nukeCallTimeout bounds the direct shell call of the nuke test. A shell that
// waits for a dialog nobody can answer would otherwise hang the whole CI job.
const nukeCallTimeout = 20 * time.Second

// shellDeleteWithTimeout runs shellDelete in a goroutine and reports blocked
// if it does not return in time. The stuck goroutine is abandoned on purpose:
// a blocked shell call cannot be cancelled, and the test binary exits anyway.
func shellDeleteWithTimeout(from []uint16, d time.Duration) (code int, aborted, blocked bool) {
	type result struct {
		code    int
		aborted bool
	}
	done := make(chan result, 1)
	go func() {
		c, a := shellDelete(from)
		done <- result{c, a}
	}()
	select {
	case r := <-done:
		return r.code, r.aborted, false
	case <-time.After(d):
		return 0, false, true
	}
}

// setBinCapacity sets MaxCapacity (MB) of the volume's bin key and restores
// the previous state at the end of the test, deleting the value again if it
// did not exist before.
func setBinCapacity(t *testing.T, guid string, mb uint32) {
	t.Helper()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, binVolumeKey+`\`+guid, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Skipf("cannot open the bin registry key: %v", err)
	}
	old, _, getErr := k.GetIntegerValue("MaxCapacity")
	if err := k.SetDWordValue("MaxCapacity", mb); err != nil {
		k.Close()
		t.Skipf("cannot set MaxCapacity: %v", err)
	}
	t.Cleanup(func() {
		defer k.Close()
		if getErr != nil {
			_ = k.DeleteValue("MaxCapacity")
			return
		}
		_ = k.SetDWordValue("MaxCapacity", uint32(old))
	})
}
