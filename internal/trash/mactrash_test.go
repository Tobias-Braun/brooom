package trash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

var _ BatchTrasher = (*macTrash)(nil)

// fakeNative stands in for the NSFileManager call: it moves items into
// <home>/.Trash exactly like the system would (unique names, resulting path)
// and reports failures for paths in fail or, for every path, err.
type fakeNative struct {
	home  string
	fail  map[string]string
	err   error
	calls []string
}

func (f *fakeNative) trash(p string) (string, error) {
	f.calls = append(f.calls, p)
	if f.err != nil {
		return "", f.err
	}
	if msg, bad := f.fail[p]; bad {
		return "", errors.New(msg)
	}
	trashDir := filepath.Join(f.home, ".Trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return "", err
	}
	name, _ := uniqueTrashName(filepath.Base(p), false, func(c string) (bool, error) {
		_, err := os.Lstat(filepath.Join(trashDir, c))
		return err == nil, nil
	})
	dst := filepath.Join(trashDir, name)
	if err := os.Rename(p, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func newFakeTrash(t *testing.T) (*macTrash, *fakeNative, string) {
	t.Helper()
	home := t.TempDir()
	f := &fakeNative{home: home}
	m := newMacTrash(home)
	m.native = f.trash
	return m, f, home
}

func TestUniqueTrashName(t *testing.T) {
	tests := []struct {
		name  string
		isDir bool
		taken []string
		want  string
	}{
		{"file.txt", false, nil, "file.txt"},
		{"file.txt", false, []string{"file.txt"}, "file 2.txt"},
		{"file.txt", false, []string{"file.txt", "file 2.txt"}, "file 3.txt"},
		{"noext", false, []string{"noext"}, "noext 2"},
		{".bashrc", false, []string{".bashrc"}, ".bashrc 2"},
		{"a.tar.gz", false, []string{"a.tar.gz"}, "a.tar 2.gz"},
		{"dir.d", true, []string{"dir.d"}, "dir.d 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.want, func(t *testing.T) {
			set := map[string]bool{}
			for _, s := range tt.taken {
				set[s] = true
			}
			got, err := uniqueTrashName(tt.name, tt.isDir, func(c string) (bool, error) { return set[c], nil })
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	boom := errors.New("boom")
	if _, err := uniqueTrashName("x", false, func(string) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Errorf("exists error not propagated: %v", err)
	}
}

func TestRemoveManyBatchWithMissingPathAndSpecialNames(t *testing.T) {
	m, f, home := newFakeTrash(t)
	root := t.TempDir()
	names := []string{"plain.txt", "q'uote.txt", "$dollar `tick`.txt", "üñí 日本.txt"}
	if runtime.GOOS != "windows" {
		// Double quotes and newlines are not valid in Windows file names.
		names = append(names, `q"uote'.txt`, "new\nline.txt")
	}
	var paths []string
	for _, n := range names {
		p := filepath.Join(root, n)
		writeFile(t, p, "12345", 0o644)
		paths = append(paths, p)
	}
	missing := filepath.Join(root, "missing")
	paths = append(paths[:2], append([]string{missing}, paths[2:]...)...)

	recs, errs := m.RemoveMany(context.Background(), paths)
	if len(recs) != len(paths) || len(errs) != len(paths) {
		t.Fatalf("lengths %d/%d", len(recs), len(errs))
	}
	for i, p := range paths {
		if p == missing {
			if !errors.Is(errs[i], fs.ErrNotExist) {
				t.Errorf("missing: err = %v, want ErrNotExist", errs[i])
			}
			continue
		}
		if errs[i] != nil {
			t.Fatalf("%q: %v", p, errs[i])
		}
		checkTrashedFile(t, recs[i], p, home)
	}
	if len(f.calls) != len(paths)-1 {
		t.Errorf("want one native call per existing path, got %d calls", len(f.calls))
	}
}

// checkTrashedFile asserts the record and disk state of a 5-byte file that
// was trashed into <home>/.Trash.
func checkTrashedFile(t *testing.T, r Record, orig, home string) {
	t.Helper()
	if r.Strategy != config.StrategyTrash || r.OriginalPath != orig || r.SizeBytes != fiveByteFileSize(t) || r.IsDir ||
		!r.Restorable || r.RemovedAt.IsZero() || r.RemovedAt.Location() != time.UTC {
		t.Errorf("unexpected record %+v", r)
	}
	if !exists(r.StoredPath) || exists(orig) {
		t.Errorf("%q: stored=%v original=%v", orig, exists(r.StoredPath), exists(orig))
	}
	if filepath.Dir(r.StoredPath) != filepath.Join(home, ".Trash") {
		t.Errorf("stored path %q outside the trash", r.StoredPath)
	}
}

func TestRemoveDirectoryAndSymlink(t *testing.T) {
	m, _, _ := newFakeTrash(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "dir", "a"), "123", 0o644)
	writeFile(t, filepath.Join(root, "dir", "sub", "b"), "45", 0o644)
	link := filepath.Join(root, "link")
	symlinkOrSkip(t, filepath.Join(root, "dir"), link)

	rec, err := m.Remove(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if rec.IsDir || !exists(filepath.Join(root, "dir", "a")) {
		t.Errorf("symlink record %+v, target must survive", rec)
	}
	wantDirSize := refSize(t, filepath.Join(root, "dir"))
	rec, err = m.Remove(context.Background(), filepath.Join(root, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.IsDir || rec.SizeBytes != wantDirSize {
		t.Errorf("dir record %+v", rec)
	}
}

func TestMacRemoveRefusals(t *testing.T) {
	m, f, home := newFakeTrash(t)
	tests := []struct {
		name string
		path string
		unix bool
	}{
		{"empty", "", false},
		{"relative", "some/file", false},
		{"filesystem root", string(filepath.Separator), false},
		{"volumes dir", "/Volumes", true},
		{"volume root", "/Volumes/Data", true},
		{"user trash", filepath.Join(home, ".Trash"), false},
		{"inside trash", filepath.Join(home, ".Trash", "x"), false},
		{"lowercase user trash", filepath.Join(home, ".trash"), false},
		{"inside lowercase trash", filepath.Join(home, ".trash", "x"), false},
		{"uppercase volume trashes", "/Volumes/Data/.TRASHES/501/x", true},
		{"volume trashes", "/Volumes/Data/.Trashes", true},
		{"inside volume trashes", "/Volumes/Data/.Trashes/501/x", true},
		{"NUL byte", filepath.Join(home, "a\x00b"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unix && runtime.GOOS == "windows" {
				t.Skip("unix-style absolute paths are not absolute on Windows")
			}
			if _, err := m.Remove(context.Background(), tt.path); err == nil {
				t.Error("want refusal")
			}
		})
	}
	if len(f.calls) != 0 {
		t.Errorf("the native trash was called for refused paths: %v", f.calls)
	}
	if _, err := m.Remove(context.Background(), filepath.Join(t.TempDir(), "nope")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("nonexistent: err = %v, want ErrNotExist", err)
	}
}

func TestPerItemNativeErrorFallsBackToHomeTrash(t *testing.T) {
	m, f, home := newFakeTrash(t)
	root := t.TempDir()
	bad := filepath.Join(root, "file.txt")
	good := filepath.Join(root, "good.txt")
	writeFile(t, bad, "abc", 0o644)
	writeFile(t, good, "abc", 0o644)
	// An earlier item of the same name forces the Finder-style " 2" suffix.
	writeFile(t, filepath.Join(home, ".Trash", "file.txt"), "old", 0o644)
	f.fail = map[string]string{bad: "cross-volume trash is not supported"}

	recs, errs := m.RemoveMany(context.Background(), []string{bad, good})
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	if want := filepath.Join(home, ".Trash", "file 2.txt"); recs[0].StoredPath != want {
		t.Errorf("fallback stored path %q, want %q", recs[0].StoredPath, want)
	}
	if exists(bad) || !exists(recs[0].StoredPath) || !exists(recs[1].StoredPath) {
		t.Error("items not moved into the trash")
	}
}

func TestNativeUnavailableFallsBack(t *testing.T) {
	home := t.TempDir()
	m := newMacTrash(home)
	m.native = func(string) (string, error) { return "", errors.New("Foundation missing") }
	p := filepath.Join(t.TempDir(), "doc.txt")
	writeFile(t, p, "abc", 0o644)
	rec, err := m.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".Trash", "doc.txt"); rec.StoredPath != want || !exists(want) || exists(p) {
		t.Errorf("record %+v", rec)
	}
}

func TestNativeResultMustBeAbsolute(t *testing.T) {
	home := t.TempDir()
	m := newMacTrash(home)
	m.native = func(string) (string, error) { return "relative/x", nil }
	p := filepath.Join(t.TempDir(), "doc.txt")
	writeFile(t, p, "abc", 0o644)
	rec, err := m.Remove(context.Background(), p)
	if err != nil || rec.StoredPath != filepath.Join(home, ".Trash", "doc.txt") {
		t.Errorf("rec %+v err %v", rec, err)
	}
}

func TestFallbackFailureNeverDeletesAndSuggestsQuarantine(t *testing.T) {
	m, f, _ := newFakeTrash(t)
	f.err = errors.New("native trash unavailable")
	m.move = func(context.Context, string, string) error {
		return &fs.PathError{Op: "rename", Path: "x", Err: syscall.EPERM}
	}
	p := filepath.Join(t.TempDir(), "keep.txt")
	writeFile(t, p, "abc", 0o644)
	rec, err := m.Remove(context.Background(), p)
	if err == nil {
		t.Fatal("want error")
	}
	// The errors quote paths with %q, which doubles Windows backslashes.
	for _, want := range []string{fmt.Sprintf("%q", p), "--trash-strategy quarantine", "native trash unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if !exists(p) || rec.StoredPath != "" {
		t.Errorf("item must be untouched, record %+v", rec)
	}
}

func TestFallbackWithoutHome(t *testing.T) {
	m := newMacTrash("")
	m.native = func(string) (string, error) { return "", errors.New("unavailable") }
	p := filepath.Join(t.TempDir(), "x")
	writeFile(t, p, "a", 0o644)
	if _, err := m.Remove(context.Background(), p); err == nil || !exists(p) {
		t.Errorf("err = %v, exists = %v", err, exists(p))
	}
}

func TestFallbackKeepsRecordOnPartialSourceRemoval(t *testing.T) {
	m, f, home := newFakeTrash(t)
	f.err = errors.New("native trash unavailable")
	m.move = func(_ context.Context, src, dst string) error {
		return &SourceNotRemovedError{Src: src, Dst: dst, Err: errors.New("busy")}
	}
	p := filepath.Join(t.TempDir(), "x")
	writeFile(t, p, "a", 0o644)
	rec, err := m.Remove(context.Background(), p)
	var partial *SourceNotRemovedError
	if !errors.As(err, &partial) || rec.StoredPath != filepath.Join(home, ".Trash", "x") {
		t.Errorf("rec %+v err %v", rec, err)
	}
}

func TestCancelledContextDoesNotTrashOrFallBack(t *testing.T) {
	m, f, home := newFakeTrash(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := filepath.Join(t.TempDir(), "x")
	writeFile(t, p, "a", 0o644)
	_, err := m.Remove(ctx, p)
	if !errors.Is(err, context.Canceled) || !exists(p) || exists(filepath.Join(home, ".Trash", "x")) || len(f.calls) != 0 {
		t.Errorf("err = %v", err)
	}
}

func TestRestore(t *testing.T) {
	newRecord := func(t *testing.T) (*macTrash, Record) {
		m, _, home := newFakeTrash(t)
		orig := filepath.Join(t.TempDir(), "sub", "file.txt")
		writeFile(t, orig, "abc", 0o644)
		rec, err := m.Remove(context.Background(), orig)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(rec.StoredPath, home) {
			t.Fatalf("stored path %q", rec.StoredPath)
		}
		return m, rec
	}

	t.Run("round trip recreates parents", func(t *testing.T) {
		m, rec := newRecord(t)
		if err := os.RemoveAll(filepath.Dir(rec.OriginalPath)); err != nil {
			t.Fatal(err)
		}
		if err := m.Restore(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if !exists(rec.OriginalPath) || exists(rec.StoredPath) {
			t.Error("item not moved back")
		}
	})
	t.Run("conflict", func(t *testing.T) {
		m, rec := newRecord(t)
		writeFile(t, rec.OriginalPath, "new", 0o644)
		if err := m.Restore(context.Background(), rec); !errors.Is(err, ErrRestoreConflict) {
			t.Errorf("err = %v", err)
		}
		if !exists(rec.StoredPath) {
			t.Error("stored copy must stay")
		}
	})
	t.Run("stored copy gone", func(t *testing.T) {
		m, rec := newRecord(t)
		_ = os.Remove(rec.StoredPath)
		if err := m.Restore(context.Background(), rec); !errors.Is(err, ErrNotRestorable) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("TCC denial on stat", func(t *testing.T) {
		m, rec := newRecord(t)
		m.lstat = func(p string) (fs.FileInfo, error) {
			return nil, &fs.PathError{Op: "lstat", Path: p, Err: syscall.EPERM}
		}
		assertTCCError(t, m.Restore(context.Background(), rec))
	})
	t.Run("EACCES on move", func(t *testing.T) {
		m, rec := newRecord(t)
		m.move = func(context.Context, string, string) error {
			return &fs.PathError{Op: "rename", Path: "x", Err: syscall.EACCES}
		}
		assertTCCError(t, m.Restore(context.Background(), rec))
	})
	t.Run("other move error is not a TCC error", func(t *testing.T) {
		m, rec := newRecord(t)
		m.move = func(context.Context, string, string) error { return errors.New("disk on fire") }
		err := m.Restore(context.Background(), rec)
		if err == nil || errors.Is(err, ErrNotRestorable) {
			t.Errorf("err = %v", err)
		}
	})
}

func assertTCCError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotRestorable) || !strings.Contains(err.Error(), trashAccessDenied) {
		t.Errorf("err = %v, want ErrNotRestorable with the Full Disk Access hint", err)
	}
}

func TestRestoreRefusesRecordsOutsideTrash(t *testing.T) {
	m, _, home := newFakeTrash(t)
	victim := filepath.Join(t.TempDir(), "victim")
	writeFile(t, victim, "x", 0o644)
	tests := []struct {
		name string
		rec  Record
	}{
		{"not in trash", Record{OriginalPath: filepath.Join(t.TempDir(), "o"), StoredPath: victim}},
		{"trash dir itself", Record{OriginalPath: filepath.Join(t.TempDir(), "o"), StoredPath: filepath.Join(home, ".Trash")}},
		{"empty stored", Record{OriginalPath: filepath.Join(t.TempDir(), "o")}},
		{"relative original", Record{OriginalPath: "o", StoredPath: filepath.Join(home, ".Trash", "x")}},
		{"relative stored", Record{OriginalPath: filepath.Join(t.TempDir(), "o"), StoredPath: ".Trash/x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := m.Restore(context.Background(), tt.rec); err == nil {
				t.Error("want refusal")
			}
		})
	}
	if !exists(victim) {
		t.Error("victim file was touched")
	}
}

func TestIsPermissionErr(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{syscall.EPERM, true},
		{syscall.EACCES, true},
		{&fs.PathError{Err: syscall.EPERM}, true},
		{fs.ErrPermission, true},
		{fs.ErrNotExist, false},
		{errors.New("x"), false},
	}
	for _, tt := range tests {
		if got := isPermissionErr(tt.err); got != tt.want {
			t.Errorf("isPermissionErr(%v) = %v", tt.err, got)
		}
	}
}

func TestNativeFailureAfterItemVanishedNeverFallsBack(t *testing.T) {
	m, _, home := newFakeTrash(t)
	gone := filepath.Join(t.TempDir(), "gone.txt")
	writeFile(t, gone, "a", 0o644)
	// The item disappears while the native call reports a failure.
	m.native = func(string) (string, error) {
		if err := os.Rename(gone, filepath.Join(home, "elsewhere.txt")); err != nil {
			return "", err
		}
		return "", errors.New("unexpected failure")
	}
	rec, err := m.Remove(context.Background(), gone)
	if err == nil || !strings.Contains(err.Error(), "may already be in the Trash") {
		t.Errorf("err = %v", err)
	}
	if rec.StoredPath != "" {
		t.Errorf("got a record: %+v", rec)
	}
}

func TestRestoreMkdirFailureIsReported(t *testing.T) {
	m, _, _ := newFakeTrash(t)
	orig := filepath.Join(t.TempDir(), "sub", "file.txt")
	writeFile(t, orig, "abc", 0o644)
	rec, err := m.Remove(context.Background(), orig)
	if err != nil {
		t.Fatal(err)
	}
	m.mkdirAll = func(string, fs.FileMode) error { return errors.New("no space") }
	if err := os.RemoveAll(filepath.Dir(orig)); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "no space") {
		t.Errorf("err = %v", err)
	}
}

// runRemoveManyWithin runs RemoveMany and fails the test if it does not return
// within a few seconds, which is how a missing deadline shows up.
func runRemoveManyWithin(t *testing.T, m *macTrash, paths []string) ([]Record, []error) {
	t.Helper()
	type out struct {
		recs []Record
		errs []error
	}
	ch := make(chan out, 1)
	go func() {
		recs, errs := m.RemoveMany(context.Background(), paths)
		ch <- out{recs, errs}
	}()
	select {
	case o := <-ch:
		return o.recs, o.errs
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveMany blocked on the hung native call")
		return nil, nil
	}
}

// TestHungNativeCallIsPendingNotSuccess injects a native call that blocks
// forever: the batch must return after the deadline, report the hung item as
// possibly pending without a record and without a ~/.Trash fallback, and skip
// the remaining items.
func TestHungNativeCallIsPendingNotSuccess(t *testing.T) {
	m, _, home := newFakeTrash(t)
	m.nativeTimeout = 50 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	calls := make(chan string, 4)
	m.native = func(p string) (string, error) {
		calls <- p
		<-release
		return "", errors.New("late")
	}
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")}
	for _, p := range paths {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	recs, errs := runRemoveManyWithin(t, m, paths)

	if !errors.Is(errs[0], errNativePending) || recs[0].StoredPath != "" {
		t.Errorf("hung item: rec %+v err %v, want pending error and no record", recs[0], errs[0])
	}
	if errs[1] == nil || !strings.Contains(errs[1].Error(), "still pending") || recs[1].StoredPath != "" {
		t.Errorf("following item: rec %+v err %v, want skipped", recs[1], errs[1])
	}
	if n := len(calls); n != 1 {
		t.Errorf("native called %d times, want 1", n)
	}
	assertNothingMoved(t, paths, home)
}

// assertNothingMoved checks that the originals are still in place and that no
// ~/.Trash fallback created the Trash directory.
func assertNothingMoved(t *testing.T, paths []string, home string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s must be untouched: %v", p, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".Trash")); err == nil {
		t.Error("no ~/.Trash fallback may run after a pending call")
	}
}
