package trash

import (
	"context"
	"encoding/json"
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

// fakeOsascript stands in for the osascript process: it moves items into
// <home>/.Trash exactly like NSFileManager would (unique names, resulting
// path) and reports per-item failures for paths in fail.
type fakeOsascript struct {
	home  string
	fail  map[string]string
	err   error
	calls [][]string
}

func (f *fakeOsascript) run(_ context.Context, argv []string) ([]byte, error) {
	var paths []string
	if err := json.Unmarshal([]byte(argv[len(argv)-1]), &paths); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, paths)
	if f.err != nil {
		return nil, f.err
	}
	trashDir := filepath.Join(f.home, ".Trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return nil, err
	}
	results := make([]jxaResult, len(paths))
	for i, p := range paths {
		results[i] = jxaResult{Path: p}
		if msg, bad := f.fail[p]; bad {
			results[i].Error = msg
			continue
		}
		name, _ := uniqueTrashName(filepath.Base(p), false, func(c string) (bool, error) {
			_, err := os.Lstat(filepath.Join(trashDir, c))
			return err == nil, nil
		})
		dst := filepath.Join(trashDir, name)
		if err := os.Rename(p, dst); err != nil {
			results[i].Error = err.Error()
			continue
		}
		results[i].Resulting = dst
	}
	return json.Marshal(results)
}

func newFakeTrash(t *testing.T) (*macTrash, *fakeOsascript, string) {
	t.Helper()
	home := t.TempDir()
	f := &fakeOsascript{home: home}
	m := newMacTrash(home)
	m.run = f.run
	return m, f, home
}

func TestBuildTrashArgv(t *testing.T) {
	paths := []string{
		`/tmp/it's "quoted"`, "/tmp/$HOME and `id`", "/tmp/new\nline", "/tmp/üñí 日本 🚀", "/tmp/with space", `/tmp/back\slash`,
	}
	argv, err := buildTrashArgv(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 5 || argv[0] != "-l" || argv[1] != "JavaScript" || argv[2] != "-e" {
		t.Fatalf("unexpected argv shape: %q", argv[:3])
	}
	var got []string
	if err := json.Unmarshal([]byte(argv[4]), &got); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(paths) {
		t.Errorf("paths do not round-trip: %q", got)
	}
	for _, p := range paths {
		if strings.Contains(argv[3], p) {
			t.Errorf("script source contains path %q", p)
		}
	}
}

func TestParseTrashOutput(t *testing.T) {
	paths := []string{"/a", "/b"}
	tests := []struct {
		name    string
		out     string
		wantErr string
	}{
		{"ok", `[{"path":"/a","resulting":"/t/a","error":""},{"path":"/b","resulting":"","error":"boom"}]` + "\n", ""},
		{"empty output", ``, "unexpected osascript output"},
		{"not json", `hello`, "unexpected osascript output"},
		{"unknown field", `[{"path":"/a","resulting":"","error":"","x":1},{"path":"/b"}]`, "unexpected osascript output"},
		{"trailing data", `[{"path":"/a"},{"path":"/b"}] extra`, "trailing data"},
		{"too few", `[{"path":"/a"}]`, "1 results for 2 paths"},
		{"wrong order", `[{"path":"/b"},{"path":"/a"}]`, "expected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := parseTrashOutput([]byte(tt.out), paths)
			if tt.wantErr == "" {
				if err != nil || len(res) != 2 || res[0].Resulting != "/t/a" || res[1].Error != "boom" {
					t.Fatalf("res=%+v err=%v", res, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
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
	names := []string{"plain.txt", `q"uote'.txt`, "$dollar `tick`.txt", "üñí 日本.txt"}
	if runtime.GOOS != "windows" {
		names = append(names, "new\nline.txt")
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
	if len(f.calls) != 1 || len(f.calls[0]) != len(paths)-1 {
		t.Errorf("want one call without the missing path, got %d calls", len(f.calls))
	}
}

// checkTrashedFile asserts the record and disk state of a 5-byte file that
// was trashed into <home>/.Trash.
func checkTrashedFile(t *testing.T, r Record, orig, home string) {
	t.Helper()
	if r.Strategy != config.StrategyTrash || r.OriginalPath != orig || r.SizeBytes != 5 || r.IsDir ||
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

func TestRemoveManyChunksAtHundred(t *testing.T) {
	m, f, _ := newFakeTrash(t)
	root := t.TempDir()
	var paths []string
	for i := 0; i < 250; i++ {
		p := filepath.Join(root, fmt.Sprintf("f%03d", i))
		writeFile(t, p, "x", 0o644)
		paths = append(paths, p)
	}
	_, errs := m.RemoveMany(context.Background(), paths)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%q: %v", paths[i], err)
		}
	}
	var sizes []int
	for _, c := range f.calls {
		sizes = append(sizes, len(c))
	}
	if fmt.Sprint(sizes) != "[100 100 50]" {
		t.Errorf("batch sizes = %v", sizes)
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
	rec, err = m.Remove(context.Background(), filepath.Join(root, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.IsDir || rec.SizeBytes != 5 {
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
		t.Errorf("osascript was called for refused paths: %v", f.calls)
	}
	if _, err := m.Remove(context.Background(), filepath.Join(t.TempDir(), "nope")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("nonexistent: err = %v, want ErrNotExist", err)
	}
}

func TestPerItemScriptErrorFallsBackToHomeTrash(t *testing.T) {
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

func TestOsascriptUnavailableFallsBack(t *testing.T) {
	home := t.TempDir()
	m := newMacTrash(home)
	m.osascript = filepath.Join(t.TempDir(), "no-such-osascript")
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

func TestFallbackFailureNeverDeletesAndSuggestsQuarantine(t *testing.T) {
	m, f, _ := newFakeTrash(t)
	f.err = errors.New("osascript unavailable")
	m.move = func(context.Context, string, string) error {
		return &fs.PathError{Op: "rename", Path: "x", Err: syscall.EPERM}
	}
	p := filepath.Join(t.TempDir(), "keep.txt")
	writeFile(t, p, "abc", 0o644)
	rec, err := m.Remove(context.Background(), p)
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{p, "--trash-strategy quarantine", "osascript unavailable"} {
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
	m.osascript = filepath.Join(t.TempDir(), "no-such-osascript")
	p := filepath.Join(t.TempDir(), "x")
	writeFile(t, p, "a", 0o644)
	if _, err := m.Remove(context.Background(), p); err == nil || !exists(p) {
		t.Errorf("err = %v, exists = %v", err, exists(p))
	}
}

func TestFallbackKeepsRecordOnPartialSourceRemoval(t *testing.T) {
	m, f, home := newFakeTrash(t)
	f.err = errors.New("osascript unavailable")
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

func TestCancelledContextDoesNotFallBack(t *testing.T) {
	m, _, home := newFakeTrash(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.run = func(context.Context, []string) ([]byte, error) {
		cancel()
		return nil, errors.New("killed")
	}
	p := filepath.Join(t.TempDir(), "x")
	writeFile(t, p, "a", 0o644)
	_, err := m.Remove(ctx, p)
	if !errors.Is(err, context.Canceled) || !exists(p) || exists(filepath.Join(home, ".Trash", "x")) {
		t.Errorf("err = %v", err)
	}
}

func TestExecScriptTimeoutAndFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a /bin/sh script as a stand-in for osascript")
	}
	dir := t.TempDir()
	slow := filepath.Join(dir, "slow")
	writeFile(t, slow, "#!/bin/sh\nexec sleep 5\n", 0o755)
	failing := filepath.Join(dir, "failing")
	writeFile(t, failing, "#!/bin/sh\necho 'script exploded' >&2\nexit 3\n", 0o755)
	ok := filepath.Join(dir, "ok")
	writeFile(t, ok, "#!/bin/sh\necho '[]'\n", 0o755)

	m := newMacTrash(t.TempDir())
	m.timeout = 200 * time.Millisecond
	tests := []struct {
		bin     string
		wantErr string
	}{
		{slow, "timed out"},
		{failing, "script exploded"},
		{ok, ""},
	}
	for _, tt := range tests {
		t.Run(filepath.Base(tt.bin), func(t *testing.T) {
			m.osascript = tt.bin
			out, err := m.execScript(context.Background(), []string{"x"})
			if tt.wantErr == "" {
				if err != nil || strings.TrimSpace(string(out)) != "[]" {
					t.Errorf("out %q err %v", out, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
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

func TestChunkFailurePartwayNeverFallsBackForTrashedItems(t *testing.T) {
	m, _, home := newFakeTrash(t)
	gone := filepath.Join(t.TempDir(), "gone.txt")
	kept := filepath.Join(t.TempDir(), "kept.txt")
	writeFile(t, gone, "a", 0o644)
	writeFile(t, kept, "b", 0o644)
	// The process trashes the first item and dies before reporting anything.
	m.run = func(context.Context, []string) ([]byte, error) {
		if err := os.Rename(gone, filepath.Join(home, "elsewhere.txt")); err != nil {
			return nil, err
		}
		return nil, errors.New("osascript timed out")
	}
	recs, errs := m.RemoveMany(context.Background(), []string{gone, kept})
	if errs[0] == nil || !strings.Contains(errs[0].Error(), "may already be in the Trash") {
		t.Errorf("gone item: err = %v", errs[0])
	}
	if recs[0].StoredPath != "" {
		t.Errorf("gone item got a record: %+v", recs[0])
	}
	// The untouched item still takes the ~/.Trash fallback.
	if errs[1] != nil || recs[1].StoredPath != filepath.Join(home, ".Trash", "kept.txt") || exists(kept) {
		t.Errorf("kept item: rec %+v err %v", recs[1], errs[1])
	}
}

func TestCompletedRunRecordsSuccessesDespiteCancelledContext(t *testing.T) {
	m, f, home := newFakeTrash(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.run = func(c context.Context, argv []string) ([]byte, error) {
		out, err := f.run(c, argv)
		cancel() // cancelled after osascript finished successfully
		return out, err
	}
	p := filepath.Join(t.TempDir(), "done.txt")
	writeFile(t, p, "a", 0o644)
	rec, err := m.Remove(ctx, p)
	if err != nil || rec.StoredPath != filepath.Join(home, ".Trash", "done.txt") || exists(p) {
		t.Errorf("rec %+v err %v", rec, err)
	}
}

func TestInvalidUTF8PathFailsOnlyThatItem(t *testing.T) {
	m, _, _ := newFakeTrash(t)
	ok := filepath.Join(t.TempDir(), "ok.txt")
	writeFile(t, ok, "a", 0o644)
	bad := filepath.Join(t.TempDir(), "bad\xff.txt")
	recs, errs := m.RemoveMany(context.Background(), []string{bad, ok})
	if errs[0] == nil || !strings.Contains(errs[0].Error(), "UTF-8") {
		t.Errorf("bad path: err = %v", errs[0])
	}
	if errs[1] != nil || recs[1].StoredPath == "" {
		t.Errorf("good path: rec %+v err %v", recs[1], errs[1])
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
