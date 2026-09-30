//go:build unix && !darwin

package trash

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// fdNow is a local-time instant so DeletionDate formatting is checked
// against the same zone the implementation formats in.
var fdNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.Local)

// newTestTrasher points HOME and XDG_DATA_HOME at temp dirs, so the real
// trash is never touched, and returns the trasher plus a scratch work dir.
func newTestTrasher(t *testing.T) (*freedesktop, string, string) {
	t.Helper()
	data := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", data)
	tr, err := newOSTrasher(Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := tr.(*freedesktop)
	f.now = func() time.Time { return fdNow }
	return f, filepath.Join(data, "Trash"), t.TempDir()
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStrategyFD(t *testing.T) {
	f, _, _ := newTestTrasher(t)
	if f.Strategy() != config.StrategyTrash {
		t.Errorf("strategy = %q", f.Strategy())
	}
}

func TestEncodePathFD(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/a/b.txt", "/a/b.txt"},
		{"/with space/x", "/with%20space/x"},
		{"/100%/x", "/100%25/x"},
		{"/a#b", "/a%23b"},
		{"/ünï", "/%C3%BCn%C3%AF"},
		{"/line\nbreak", "/line%0Abreak"},
		{"/tilde~-_.", "/tilde~-_."},
	}
	for _, tt := range tests {
		if got := encodePath(tt.in); got != tt.want {
			t.Errorf("encodePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHomeTrashDirFD(t *testing.T) {
	tests := []struct {
		name, xdg, home, want string
		wantErr               bool
	}{
		{"xdg wins", "/x/data", "/h", "/x/data/Trash", false},
		{"empty xdg ignored", "", "/h", "/h/.local/share/Trash", false},
		{"relative xdg ignored", "rel/data", "/h", "/h/.local/share/Trash", false},
		{"nothing set", "", "", "", true},
		{"relative xdg and no home", "rel", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"XDG_DATA_HOME": tt.xdg, "HOME": tt.home}
			got, err := homeTrashDir(func(k string) string { return env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "quarantine") {
				t.Errorf("error does not suggest quarantine: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewOSTrasherWithoutHomeFD(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if _, err := newOSTrasher(Options{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRemoveFileWritesTrashInfoFD(t *testing.T) {
	f, trash, work := newTestTrasher(t)
	p := filepath.Join(work, "we ird%#ü\nname.txt")
	writeFile(t, p, "hello", 0o644)

	rec, err := f.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if exists(p) {
		t.Error("original still present")
	}
	name := "we ird%#ü\nname.txt"
	if rec.StoredPath != filepath.Join(trash, "files", name) || rec.InfoPath != filepath.Join(trash, "info", name+".trashinfo") {
		t.Errorf("paths: %+v", rec)
	}
	if got := readFile(t, rec.StoredPath); got != "hello" {
		t.Errorf("stored content %q", got)
	}
	want := "[Trash Info]\nPath=" + encodePath(p) + "\nDeletionDate=2026-03-04T05:06:07\n"
	if got := readFile(t, rec.InfoPath); got != want {
		t.Errorf("info = %q, want %q", got, want)
	}
	if rec.Strategy != config.StrategyTrash || rec.OriginalPath != p || rec.SizeBytes != 5 || rec.IsDir || !rec.Restorable || !rec.RemovedAt.Equal(fdNow) {
		t.Errorf("record: %+v", rec)
	}
	for _, d := range []string{trash, filepath.Join(trash, "files"), filepath.Join(trash, "info")} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", d, fi, err)
		}
	}
	if fi, _ := os.Stat(rec.InfoPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("info mode %v", fi.Mode())
	}
}

func TestUniqueNamesOnCollisionFD(t *testing.T) {
	f, trash, work := newTestTrasher(t)
	// Pre-existing item, info-only entry and a taken .2 exercise both
	// collision sources named in the spec.
	writeFile(t, filepath.Join(trash, "files", "foo"), "old", 0o644)
	writeFile(t, filepath.Join(trash, "info", "foo.2.trashinfo"), "x", 0o600)
	writeFile(t, filepath.Join(trash, "files", "foo.3"), "old", 0o644)
	p := filepath.Join(work, "foo")
	writeFile(t, p, "new", 0o644)
	rec, err := f.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(rec.StoredPath) != "foo.4" {
		t.Errorf("stored %q", rec.StoredPath)
	}
	if got := readFile(t, filepath.Join(trash, "files", "foo")); got != "old" {
		t.Error("existing item was overwritten")
	}
}

func TestConcurrentEqualBasenamesFD(t *testing.T) {
	f, trash, work := newTestTrasher(t)
	const n = 16
	var wg sync.WaitGroup
	recs := make([]Record, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(work, string(rune('a'+i)), "same")
		writeFile(t, p, string(rune('a'+i)), 0o644)
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i], errs[i] = f.Remove(context.Background(), p)
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := range recs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if seen[recs[i].StoredPath] {
			t.Fatalf("duplicate name %s", recs[i].StoredPath)
		}
		seen[recs[i].StoredPath] = true
		if got := readFile(t, recs[i].StoredPath); got != string(rune('a'+i)) {
			t.Errorf("content of %s = %q", recs[i].StoredPath, got)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(trash, "info"))
	if len(entries) != n {
		t.Errorf("%d info files, want %d", len(entries), n)
	}
}

func TestRemoveSymlinkAndDirFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	target := filepath.Join(work, "target")
	writeFile(t, filepath.Join(target, "keep"), "k", 0o644)
	link := filepath.Join(work, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	rec, err := f.Remove(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(rec.StoredPath)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("link not trashed as link: %v %v", fi, err)
	}
	if !exists(filepath.Join(target, "keep")) {
		t.Error("link target was touched")
	}

	rec, err = f.Remove(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.IsDir || rec.SizeBytes != 1 || !exists(filepath.Join(rec.StoredPath, "keep")) {
		t.Errorf("dir record %+v", rec)
	}
}

func TestDirectorySizesFD(t *testing.T) {
	f, trash, work := newTestTrasher(t)
	d := filepath.Join(work, "dir one")
	writeFile(t, filepath.Join(d, "f"), "12345", 0o644)

	// Without an existing cache none is created.
	rec, err := f.Remove(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(trash, "directorysizes")) {
		t.Fatal("directorysizes created although absent")
	}
	if err := f.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(trash, "directorysizes")
	writeFile(t, cache, "9 1 other\n", 0o600)
	rec, err = f.Remove(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(readFile(t, cache)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "5 ") || !strings.HasSuffix(lines[1], " dir%20one") {
		t.Fatalf("cache = %q", lines)
	}
	if err := f.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, cache); got != "9 1 other\n" {
		t.Errorf("cache after restore = %q", got)
	}
}

func TestRestoreRoundTripFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	p := filepath.Join(work, "deep", "er", "file")
	writeFile(t, p, "data", 0o644)
	rec, err := f.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(work, "deep")); err != nil {
		t.Fatal(err)
	}
	if err := f.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != "data" {
		t.Errorf("restored %q", got)
	}
	if exists(rec.InfoPath) || exists(rec.StoredPath) {
		t.Error("trash entry left behind")
	}
}

func TestRestoreMissingInfoIsFineFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	p := filepath.Join(work, "f")
	writeFile(t, p, "x", 0o644)
	rec, _ := f.Remove(context.Background(), p)
	if err := os.Remove(rec.InfoPath); err != nil {
		t.Fatal(err)
	}
	if err := f.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRefusalsFD(t *testing.T) {
	f, trash, work := newTestTrasher(t)
	p := filepath.Join(work, "f")
	writeFile(t, p, "x", 0o644)
	rec, err := f.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(work, "victim")
	writeFile(t, outside, "v", 0o644)

	with := func(mod func(*Record)) Record { r := rec; mod(&r); return r }
	tests := []struct {
		name string
		rec  Record
		want error // nil: any error is acceptable
	}{
		{"not restorable", with(func(r *Record) { r.Restorable = false }), ErrNotRestorable},
		{"outside trash", with(func(r *Record) { r.StoredPath = outside; r.InfoPath = "" }), nil},
		{"traversal", with(func(r *Record) { r.StoredPath = filepath.Join(trash, "files", "..", "..", "victim") }), nil},
		{"wrong info", with(func(r *Record) { r.InfoPath = outside }), nil},
		{"relative original", with(func(r *Record) { r.OriginalPath = "rel" }), nil},
		{"root original", with(func(r *Record) { r.OriginalPath = "/" }), nil},
		{"info dir instead of files", with(func(r *Record) { r.StoredPath = filepath.Join(trash, "info", "f.trashinfo") }), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := f.Restore(context.Background(), tt.rec)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
	if !exists(outside) || !exists(rec.StoredPath) {
		t.Error("refused restore touched files")
	}
}

func TestRestoreConflictAndMissingFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	p := filepath.Join(work, "f")
	writeFile(t, p, "x", 0o644)
	rec, _ := f.Remove(context.Background(), p)

	writeFile(t, p, "new", 0o644)
	if err := f.Restore(context.Background(), rec); !errors.Is(err, ErrRestoreConflict) {
		t.Errorf("err = %v", err)
	}
	if got := readFile(t, p); got != "new" || !exists(rec.StoredPath) {
		t.Error("conflict overwrote or moved something")
	}

	// A dangling symlink at the original path is a conflict as well.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, "nowhere"), p); err != nil {
		t.Fatal(err)
	}
	if err := f.Restore(context.Background(), rec); !errors.Is(err, ErrRestoreConflict) {
		t.Errorf("dangling link: err = %v", err)
	}
	_ = os.Remove(p)

	if err := os.Remove(rec.StoredPath); err != nil {
		t.Fatal(err)
	}
	if err := f.Restore(context.Background(), rec); !errors.Is(err, ErrNotRestorable) {
		t.Errorf("missing stored: err = %v", err)
	}
}

func TestFreedesktopRemoveRefusals(t *testing.T) {
	f, trash, work := newTestTrasher(t)
	if err := ensureTrashDir(trash, false); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(trash, "files", "x")
	writeFile(t, inside, "x", 0o644)
	tests := []struct {
		name, path string
		notExist   bool
	}{
		{"empty", "", false},
		{"relative", "a/b", false},
		{"root", "/", false},
		{"trash dir", trash, false},
		{"files dir", filepath.Join(trash, "files"), false},
		{"inside trash", inside, false},
		{"ancestor of trash", filepath.Dir(trash), false},
		{"nonexistent", filepath.Join(work, "missing"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.Remove(context.Background(), tt.path)
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.notExist && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("err = %v", err)
			}
		})
	}
	if !exists(inside) {
		t.Error("refusal moved the item")
	}
}

func TestXDGOverrideAndRelativeIgnoredFD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "relative/dir")
	tr, err := newOSTrasher(Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "f")
	writeFile(t, p, "x", 0o644)
	rec, err := tr.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "share", "Trash", "files", "f"); rec.StoredPath != want {
		t.Errorf("stored %q, want %q", rec.StoredPath, want)
	}
}

type fakeInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (i fakeInfo) Mode() fs.FileMode { return i.mode }
func (i fakeInfo) IsDir() bool       { return i.mode.IsDir() }

func TestTopdirCandidatesFD(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
		err  error
		want []string
	}{
		{"valid sticky dir", fs.ModeDir | fs.ModeSticky | 0o777, nil, []string{"/m/.Trash/7", "/m/.Trash-7"}},
		{"no sticky bit", fs.ModeDir | 0o777, nil, []string{"/m/.Trash-7"}},
		{"symlink", fs.ModeSymlink | fs.ModeSticky | 0o777, nil, []string{"/m/.Trash-7"}},
		{"regular file", fs.ModeSticky | 0o777, nil, []string{"/m/.Trash-7"}},
		{"missing", 0, fs.ErrNotExist, []string{"/m/.Trash-7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lstat := func(string) (fs.FileInfo, error) { return fakeInfo{mode: tt.mode}, tt.err }
			got := topdirCandidates("/m", 7, lstat)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindTopdirFD(t *testing.T) {
	devs := map[string]uint64{"/": 1, "/mnt": 1, "/mnt/usb": 2, "/mnt/usb/a": 2, "/mnt/usb/a/b": 2}
	dev := func(p string) (uint64, error) { return devs[p], nil }
	tests := []struct{ dir, want string }{
		{"/mnt/usb/a/b", "/mnt/usb"},
		{"/mnt/usb", "/mnt/usb"},
		{"/mnt", "/"},
	}
	for _, tt := range tests {
		got, err := findTopdir(tt.dir, dev)
		if err != nil || got != tt.want {
			t.Errorf("findTopdir(%q) = %q, %v; want %q", tt.dir, got, err, tt.want)
		}
	}
	if _, err := findTopdir("/x", func(string) (uint64, error) { return 0, fs.ErrNotExist }); err == nil {
		t.Error("expected error")
	}
}

// fakeMount makes everything below mount report another device, so the
// topdir code path runs although temp dirs share one real filesystem.
func fakeMount(f *freedesktop, mount string) {
	real := f.deviceOf
	f.deviceOf = func(p string) (uint64, error) {
		d, err := real(p)
		if p == mount || isWithin(mount, p) {
			d += 1000
		}
		return d, err
	}
}

func TestTopdirTrashFD(t *testing.T) {
	f, home, work := newTestTrasher(t)
	mount := filepath.Join(work, "mnt")
	fakeMount(f, mount)
	p := filepath.Join(mount, "proj", "my file")
	writeFile(t, p, "abc", 0o644)

	rec, err := f.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	want := filepath.Join(mount, ".Trash-"+itoa(uid))
	if rec.StoredPath != filepath.Join(want, "files", "my file") {
		t.Fatalf("stored %q, want under %q", rec.StoredPath, want)
	}
	if fi, _ := os.Stat(want); fi.Mode().Perm() != 0o700 {
		t.Errorf("topdir trash mode %v", fi.Mode())
	}
	if got := readFile(t, rec.InfoPath); !strings.Contains(got, "Path=proj/my%20file\n") {
		t.Errorf("info = %q", got)
	}
	if exists(filepath.Join(home, "files", "my file")) {
		t.Error("item went to the home trash")
	}
	if err := f.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if readFile(t, p) != "abc" {
		t.Error("restore failed")
	}
}

func TestTopdirSharedTrashUsedWhenValidFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	mount := filepath.Join(work, "mnt")
	fakeMount(f, mount)
	shared := filepath.Join(mount, ".Trash")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, fs.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(mount, "f")
	writeFile(t, p, "x", 0o644)
	rec, err := f.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(shared, itoa(os.Getuid()), "files", "f"); rec.StoredPath != want {
		t.Errorf("stored %q, want %q", rec.StoredPath, want)
	}
	if err := f.Restore(context.Background(), rec); err != nil {
		t.Errorf("restore from shared trash: %v", err)
	}
}

func TestTopdirIgnoresInvalidSharedTrashFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	mount := filepath.Join(work, "mnt")
	fakeMount(f, mount)
	if err := os.MkdirAll(filepath.Join(work, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	// .Trash as a symlink must never be written through.
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, "elsewhere"), filepath.Join(mount, ".Trash")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(mount, "f")
	writeFile(t, p, "x", 0o644)
	rec, err := f.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(mount, ".Trash-"+itoa(os.Getuid())); !strings.HasPrefix(rec.StoredPath, want) {
		t.Errorf("stored %q", rec.StoredPath)
	}
	if entries, _ := os.ReadDir(filepath.Join(work, "elsewhere")); len(entries) != 0 {
		t.Error("wrote through the .Trash symlink")
	}
}

func TestTopdirFallsBackToHomeTrashFD(t *testing.T) {
	f, home, work := newTestTrasher(t)
	mount := filepath.Join(work, "mnt")
	fakeMount(f, mount)
	// A regular file where .Trash-$uid should be makes creation impossible
	// even for root, which ignores permission bits.
	writeFile(t, filepath.Join(mount, ".Trash-"+itoa(os.Getuid())), "block", 0o644)
	p := filepath.Join(mount, "f")
	writeFile(t, p, "x", 0o644)
	rec, err := f.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.StoredPath != filepath.Join(home, "files", "f") {
		t.Errorf("stored %q", rec.StoredPath)
	}
	if got := readFile(t, rec.InfoPath); !strings.Contains(got, "Path="+encodePath(p)+"\n") {
		t.Errorf("home trash info must hold the absolute path: %q", got)
	}
}

func TestTopdirRefusesItsOwnTrashFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	mount := filepath.Join(work, "mnt")
	fakeMount(f, mount)
	own := filepath.Join(mount, ".Trash-"+itoa(os.Getuid()))
	writeFile(t, filepath.Join(own, "files", "x"), "x", 0o644)
	for _, p := range []string{own, filepath.Join(own, "files", "x")} {
		if _, err := f.Remove(context.Background(), p); err == nil {
			t.Errorf("%s was trashed", p)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
