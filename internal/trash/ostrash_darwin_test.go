//go:build darwin

package trash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newRealMacTrash returns the real trasher and registers a cleanup that
// removes every item this test trashed, and only those, from the Trash.
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
			// Best effort: without Full Disk Access the item stays, which is
			// the documented TCC limitation, not a test failure.
			_ = os.RemoveAll(r.StoredPath)
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
	m, track := newRealMacTrash(t)
	root := t.TempDir()
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
	if !isInsideTrash(rec.StoredPath) || rec.SizeBytes != 5 || !rec.Restorable {
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
	m, track := newRealMacTrash(t)
	root := t.TempDir()
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
	m, track := newRealMacTrash(t)
	root := t.TempDir()
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

func TestDarwinTrashFallbackWithoutOsascript(t *testing.T) {
	m, track := newRealMacTrash(t)
	m.osascript = filepath.Join(t.TempDir(), "missing-osascript")
	orig := filepath.Join(t.TempDir(), "fallback.txt")
	writeFile(t, orig, "x", 0o644)
	rec, err := m.Remove(context.Background(), orig)
	if err != nil {
		t.Skipf("fallback needs a writable ~/.Trash: %v", err)
	}
	track(rec)
	if exists(orig) {
		t.Error("original still exists")
	}
}
