//go:build darwin

package trash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newRealMacTrash returns the real trasher and registers a cleanup that puts
// every item this test trashed, and only those, back where it came from. Tests
// must create their temporary directory before calling it: cleanups run last
// in first out, so the directory is removed after the items were restored.
func newRealMacTrash(t *testing.T) (*macTrash, func(Record)) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	m := newMacTrash(home)
	var trashed []Record
	t.Cleanup(func() {
		for _, r := range trashed {
			if err := m.Restore(context.Background(), r); err != nil && !errors.Is(err, ErrNotRestorable) && !errors.Is(err, ErrRestoreConflict) {
				t.Logf("cleanup restore of %q: %v", r.OriginalPath, err)
			}
		}
	})
	return m, func(r Record) { trashed = append(trashed, r) }
}

// skipIfTrashDenied skips tests that inspect StoredPath when TCC blocks it.
func skipIfTrashDenied(t *testing.T, stored string) {
	t.Helper()
	if _, err := os.Lstat(stored); errors.Is(err, os.ErrPermission) || isPermissionErr(err) {
		t.Skip(trashAccessDenied)
	}
}

func TestDarwinTrashRoundTrip(t *testing.T) {
	root := t.TempDir()
	m, track := newRealMacTrash(t)
	orig := filepath.Join(root, "it's \"q\" $x `y` üñí.txt")
	writeFile(t, orig, "hello", 0o644)
	rec, err := m.Remove(context.Background(), orig)
	if err != nil {
		t.Fatal(err)
	}
	track(rec)
	if exists(orig) {
		t.Error("original still exists")
	}
	if !isInsideTrash(rec.StoredPath) || rec.SizeBytes != fiveByteFileSize(t) || !rec.Restorable {
		t.Errorf("record %+v", rec)
	}
	skipIfTrashDenied(t, rec.StoredPath)
	if !exists(rec.StoredPath) {
		t.Fatal("item missing at StoredPath")
	}
	writeFile(t, orig, "conflict", 0o644)
	if err := m.Restore(context.Background(), rec); !errors.Is(err, ErrRestoreConflict) {
		t.Errorf("conflict: err = %v", err)
	}
	if err := os.Remove(orig); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if !exists(orig) {
		t.Error("not restored")
	}
}

func TestDarwinTrashSymlinkKeepsTarget(t *testing.T) {
	root := t.TempDir()
	m, track := newRealMacTrash(t)
	writeFile(t, filepath.Join(root, "dir", "a"), "1", 0o644)
	link := filepath.Join(root, "link")
	symlinkOrSkip(t, filepath.Join(root, "dir"), link)
	rec, err := m.Remove(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	track(rec)
	if rec.IsDir || !exists(filepath.Join(root, "dir", "a")) {
		t.Errorf("record %+v: target must survive and link is not a dir", rec)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("link still present: %v", err)
	}
}

func TestDarwinTrashBatchWithMissingPath(t *testing.T) {
	root := t.TempDir()
	m, track := newRealMacTrash(t)
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	writeFile(t, a, "1", 0o644)
	writeFile(t, b, "22", 0o644)
	paths := []string{a, filepath.Join(root, "missing"), b}
	recs, errs := m.RemoveMany(context.Background(), paths)
	for i := range recs {
		if errs[i] == nil {
			track(recs[i])
		}
	}
	if errs[0] != nil || errs[2] != nil || !errors.Is(errs[1], os.ErrNotExist) {
		t.Fatalf("errs = %v", errs)
	}
	if exists(a) || exists(b) {
		t.Error("originals still exist")
	}
}

func TestDarwinTrashDirectory(t *testing.T) {
	root := t.TempDir()
	m, track := newRealMacTrash(t)
	dir := filepath.Join(root, "some dir")
	writeFile(t, filepath.Join(dir, "sub", "a"), "123", 0o644)
	wantSize := refSize(t, dir)
	rec, err := m.Remove(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	track(rec)
	if !rec.IsDir || rec.SizeBytes != wantSize || !isInsideTrash(rec.StoredPath) || exists(dir) {
		t.Errorf("record %+v", rec)
	}
}
