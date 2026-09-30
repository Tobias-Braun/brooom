package trash

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

var fixedNow = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// newTestQuarantine builds a quarantine trasher below a temp dir and returns
// it with the temp root (holding "q" and the items to remove).
func newTestQuarantine(t *testing.T, session string) (*quarantine, string) {
	t.Helper()
	root := t.TempDir()
	tr, err := newQuarantine(Options{SessionID: session, QuarantineDir: filepath.Join(root, "q")})
	if err != nil {
		t.Fatal(err)
	}
	q := tr.(*quarantine)
	q.now = func() time.Time { return fixedNow }
	return q, root
}

func loadManifest(t *testing.T, q *quarantine) *QuarantineManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(q.dir, q.session, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m QuarantineManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func TestNewQuarantineValidation(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		opts Options
	}{
		{"no session", Options{QuarantineDir: dir}},
		{"no dir", Options{SessionID: "s"}},
		{"slash", Options{SessionID: "a/b", QuarantineDir: dir}},
		{"backslash", Options{SessionID: `a\b`, QuarantineDir: dir}},
		{"dotdot", Options{SessionID: "..", QuarantineDir: dir}},
		{"dot", Options{SessionID: ".", QuarantineDir: dir}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(config.StrategyQuarantine, tt.opts); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestQuarantineRoundTrip(t *testing.T) {
	type setup func(t *testing.T, p string)
	tests := []struct {
		name    string
		setup   setup
		size    int64
		isDir   bool
		isLink  bool
		needsSy bool
	}{
		{"file", func(t *testing.T, p string) { writeFile(t, p, "12345", 0o644) }, 5, false, false, false},
		{"nested dir", func(t *testing.T, p string) {
			writeFile(t, filepath.Join(p, "a", "b", "c"), "123", 0o644)
			writeFile(t, filepath.Join(p, "d"), "12", 0o644)
		}, 5, true, false, false},
		{"empty dir", func(t *testing.T, p string) {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}, 0, true, false, false},
		{"symlink to file", func(t *testing.T, p string) {
			writeFile(t, p+".real", "abcdef", 0o644)
			symlinkOrSkip(t, p+".real", p)
		}, -1, false, true, true},
		{"symlink to dir", func(t *testing.T, p string) {
			writeFile(t, filepath.Join(p+".real", "f"), "abcdef", 0o644)
			symlinkOrSkip(t, p+".real", p)
		}, -1, false, true, true},
		{"dangling symlink", func(t *testing.T, p string) {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, filepath.Join(filepath.Dir(p), "nowhere"), p)
		}, -1, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, root := newTestQuarantine(t, "sess1")
			p := filepath.Join(root, "work", "item")
			tt.setup(t, p)
			var wantTarget string
			if tt.isLink {
				wantTarget, _ = os.Readlink(p)
			}
			rec, err := q.Remove(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if exists(p) {
				t.Fatal("original still exists")
			}
			wantStored := filepath.Join(q.dir, "sess1", "1", "item")
			if rec.Strategy != config.StrategyQuarantine || rec.OriginalPath != p || rec.StoredPath != wantStored ||
				rec.IsDir != tt.isDir || !rec.Restorable || !rec.RemovedAt.Equal(fixedNow) {
				t.Errorf("unexpected record %+v", rec)
			}
			if tt.size >= 0 && rec.SizeBytes != tt.size {
				t.Errorf("size = %d, want %d", rec.SizeBytes, tt.size)
			}
			if tt.isLink {
				if got, err := os.Readlink(wantStored); err != nil || got != wantTarget {
					t.Errorf("stored link = %q, %v; want %q", got, err, wantTarget)
				}
				if _, err := os.Lstat(p + ".real"); err != nil && tt.name != "dangling symlink" {
					t.Error("link target was touched")
				}
			}
			m := loadManifest(t, q)
			if len(m.Items) != 1 || m.Items[0].IsSymlink != tt.isLink || m.Items[0].StoredPath != "1/item" {
				t.Errorf("manifest items = %+v", m.Items)
			}

			if err := q.Restore(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(p); err != nil {
				t.Fatalf("not restored: %v", err)
			}
			if exists(filepath.Join(q.dir, "sess1", "1")) {
				t.Error("emptied <n> dir not removed")
			}
			if m := loadManifest(t, q); len(m.Items) != 0 {
				t.Errorf("manifest still lists %+v", m.Items)
			}
		})
	}
}

func TestQuarantineManifestContent(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	p := filepath.Join(root, "a", "f.txt")
	writeFile(t, p, "hello", 0o644)
	if _, err := q.Remove(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	m := loadManifest(t, q)
	if m.Version != 1 || m.SessionID != "sess" || !m.CreatedAt.Equal(fixedNow) || len(m.Items) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	it := m.Items[0]
	if it.N != 1 || it.OriginalPath != p || it.StoredPath != "1/f.txt" || it.SizeBytes != 5 || it.IsDir || it.IsSymlink || !it.RemovedAt.Equal(fixedNow) {
		t.Errorf("item = %+v", it)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(q.dir, "sess", ManifestName))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("manifest mode = %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Join(q.dir, "sess"))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("session dir mode = %v", di.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Join(q.dir, "sess"))
	for _, e := range entries {
		if e.Name() != "1" && e.Name() != ManifestName {
			t.Errorf("stray file %q (temp manifest not cleaned up)", e.Name())
		}
	}
}

func TestQuarantineSameBasenameAndResume(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	a := filepath.Join(root, "x", "same")
	b := filepath.Join(root, "y", "same")
	writeFile(t, a, "A", 0o644)
	writeFile(t, b, "B", 0o644)
	ra, err := q.Remove(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := q.Remove(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if ra.StoredPath == rb.StoredPath {
		t.Fatal("collision")
	}

	// A new process for the same session continues the counter.
	q2, err := newQuarantine(Options{SessionID: "sess", QuarantineDir: q.dir})
	if err != nil {
		t.Fatal(err)
	}
	c := filepath.Join(root, "z", "same")
	writeFile(t, c, "C", 0o644)
	rc, err := q2.Remove(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(q.dir, "sess", "3", "same"); rc.StoredPath != want {
		t.Errorf("resumed stored path = %q, want %q", rc.StoredPath, want)
	}
	m := loadManifest(t, q)
	if len(m.Items) != 3 || m.Items[2].N != 3 {
		t.Errorf("manifest = %+v", m.Items)
	}
	if !m.CreatedAt.Equal(fixedNow) {
		t.Error("created_at changed on resume")
	}
}

func TestQuarantineSkipsExistingNDirWithoutManifest(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	if err := os.MkdirAll(filepath.Join(q.dir, "sess", "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "f")
	writeFile(t, p, "x", 0o644)
	rec, err := q.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(q.dir, "sess", "2", "f"); rec.StoredPath != want {
		t.Errorf("stored = %q, want %q", rec.StoredPath, want)
	}
}

func TestQuarantineCorruptManifestRefusesRemove(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	writeFile(t, filepath.Join(q.dir, "sess", ManifestName), "{not json", 0o600)
	p := filepath.Join(root, "f")
	writeFile(t, p, "x", 0o644)
	if _, err := q.Remove(context.Background(), p); err == nil {
		t.Fatal("expected error")
	}
	if !exists(p) {
		t.Error("item removed despite manifest failure")
	}
}

func TestQuarantineCrossDevice(t *testing.T) {
	failCrossDevice(t)
	q, root := newTestQuarantine(t, "sess")
	p := filepath.Join(root, "tree")
	writeFile(t, filepath.Join(p, "sub", "f"), "data", 0o640)
	mtime := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(p, "sub", "f"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	rec, err := q.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if exists(p) {
		t.Fatal("source not removed after cross-device copy")
	}
	stored := filepath.Join(rec.StoredPath, "sub", "f")
	fi, err := os.Stat(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mtime) {
		t.Errorf("mtime = %v, want %v", fi.ModTime(), mtime)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	// Restore takes the same fallback in reverse.
	if err := q.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(p, "sub", "f")); string(b) != "data" {
		t.Errorf("restored content = %q", b)
	}
}

func TestQuarantineCrossDeviceFailureLeavesSource(t *testing.T) {
	failCrossDevice(t)
	q, root := newTestQuarantine(t, "sess")
	p := filepath.Join(root, "tree")
	writeFile(t, filepath.Join(p, "f"), "data", 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel once Remove has started: the first copy step observes it.
	orig := renameFunc
	renameFunc = func(o, n string) error { cancel(); return orig(o, n) }
	if _, err := q.Remove(ctx, p); err == nil {
		t.Fatal("expected error")
	}
	if !exists(filepath.Join(p, "f")) {
		t.Error("source lost")
	}
	if exists(filepath.Join(q.dir, "sess", "1")) {
		t.Error("partial quarantine directory left behind")
	}
}

func TestRestoreConflictAndMissing(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	p := filepath.Join(root, "f")
	writeFile(t, p, "orig", 0o644)
	rec, err := q.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, p, "new", 0o644)
	if err := q.Restore(context.Background(), rec); !errors.Is(err, ErrRestoreConflict) {
		t.Fatalf("err = %v, want ErrRestoreConflict", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Error("existing file overwritten")
	}
	if !exists(rec.StoredPath) {
		t.Error("stored copy lost on conflict")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(rec.StoredPath); err != nil {
		t.Fatal(err)
	}
	if err := q.Restore(context.Background(), rec); !errors.Is(err, ErrNotRestorable) {
		t.Fatalf("err = %v, want ErrNotRestorable", err)
	}
	notRestorable := rec
	notRestorable.Restorable = false
	if err := q.Restore(context.Background(), notRestorable); !errors.Is(err, ErrNotRestorable) {
		t.Fatalf("err = %v, want ErrNotRestorable", err)
	}
}

func TestRestoreDanglingSymlinkCountsAsConflict(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	p := filepath.Join(root, "f")
	writeFile(t, p, "orig", 0o644)
	rec, err := q.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(root, "nowhere"), p)
	if err := q.Restore(context.Background(), rec); !errors.Is(err, ErrRestoreConflict) {
		t.Fatalf("err = %v, want ErrRestoreConflict", err)
	}
}

func TestRestoreRecreatesParents(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	p := filepath.Join(root, "deep", "er", "f")
	writeFile(t, p, "x", 0o644)
	rec, err := q.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "deep")); err != nil {
		t.Fatal(err)
	}
	if err := q.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "x" {
		t.Errorf("content = %q", b)
	}
}

func TestRestoreFromEarlierSession(t *testing.T) {
	q1, root := newTestQuarantine(t, "old")
	p := filepath.Join(root, "f")
	writeFile(t, p, "x", 0o644)
	rec, err := q1.Remove(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	q2, err := newQuarantine(Options{SessionID: "new", QuarantineDir: q1.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := q2.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if m := loadManifest(t, q1); len(m.Items) != 0 {
		t.Errorf("old session manifest not updated: %+v", m.Items)
	}
}

// Records come from user-editable manifests. Every forged StoredPath must be
// refused with ErrNotRestorable before anything moves.
func TestRestoreRefusesForgedStoredPath(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	victim := filepath.Join(root, "outside", "secret.txt")
	writeFile(t, victim, "secret", 0o644)
	// Give the quarantine a real item so the layout exists.
	real := filepath.Join(root, "real")
	writeFile(t, real, "r", 0o644)
	if _, err := q.Remove(context.Background(), real); err != nil {
		t.Fatal(err)
	}
	// A directory holding a file, reachable by symlinked n / session dirs.
	evil := filepath.Join(root, "evil")
	writeFile(t, filepath.Join(evil, "secret.txt"), "evil", 0o644)
	symlinkedN := filepath.Join(q.dir, "sess", "7")
	symlinkedSession := filepath.Join(q.dir, "linked")

	tests := []struct {
		name   string
		stored string
		link   func(t *testing.T)
	}{
		{"outside path", victim, nil},
		{"quarantine root itself", q.dir, nil},
		{"session dir itself", filepath.Join(q.dir, "sess"), nil},
		{"manifest file", filepath.Join(q.dir, "sess", ManifestName), nil},
		{"too shallow", filepath.Join(q.dir, "sess", "1"), nil},
		{"too deep", filepath.Join(q.dir, "sess", "1", "a", "b"), nil},
		{"dotdot escape", filepath.Join(q.dir, "sess", "1", "..", "..", "..", "outside", "secret.txt"), nil},
		{"non numeric n", filepath.Join(q.dir, "sess", "x", "f"), nil},
		{"relative", filepath.Join("q", "sess", "1", "real"), nil},
		{"empty", "", nil},
		{"sibling prefix", q.dir + "x" + string(filepath.Separator) + "sess" + string(filepath.Separator) + "1" + string(filepath.Separator) + "f", nil},
		{"symlinked n dir", filepath.Join(symlinkedN, "secret.txt"), func(t *testing.T) { symlinkOrSkip(t, evil, symlinkedN) }},
		{"symlinked session dir", filepath.Join(symlinkedSession, "1", "secret.txt"), func(t *testing.T) {
			writeFile(t, filepath.Join(evil, "1", "secret.txt"), "evil", 0o644)
			symlinkOrSkip(t, evil, symlinkedSession)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.link != nil {
				tt.link(t)
			}
			target := filepath.Join(root, "victim-dest", "restored")
			rec := Record{
				Strategy: config.StrategyQuarantine, OriginalPath: target,
				StoredPath: tt.stored, Restorable: true,
			}
			err := q.Restore(context.Background(), rec)
			if !errors.Is(err, ErrNotRestorable) {
				t.Fatalf("err = %v, want ErrNotRestorable", err)
			}
			if exists(target) {
				t.Error("something was moved to the original path")
			}
			if b, _ := os.ReadFile(victim); string(b) != "secret" {
				t.Error("outside file was touched")
			}
			if b, _ := os.ReadFile(filepath.Join(evil, "secret.txt")); string(b) != "evil" {
				t.Error("symlink target file was touched")
			}
		})
	}
}
