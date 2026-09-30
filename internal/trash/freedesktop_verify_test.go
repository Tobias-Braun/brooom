//go:build unix && !darwin

package trash

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forgedRecord builds the manifest record of the attack in the issue: the
// stored path names a file the attacker wants moved into the repository.
func forgedRecord(stored, original string) Record {
	return Record{
		StoredPath:   stored,
		OriginalPath: original,
		Restorable:   true,
	}
}

func TestRestoreRefusesSymlinkedFilesDirFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	mount := filepath.Join(work, "mnt")
	fakeMount(f, mount)
	root := filepath.Join(mount, ".Trash-"+itoa(f.uid))
	outside := filepath.Join(work, "outside")
	secret := filepath.Join(outside, "secret.txt")
	writeFile(t, secret, "secret", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "files")); err != nil {
		t.Fatal(err)
	}
	rec := forgedRecord(filepath.Join(root, "files", "secret.txt"), filepath.Join(work, "repo", "stolen.txt"))
	if err := f.Restore(context.Background(), rec); err == nil {
		t.Fatal("restore through a symlinked files directory was accepted")
	}
	if !exists(secret) || exists(rec.OriginalPath) {
		t.Error("the out-of-scope file was moved")
	}
}

func TestRestoreRefusesSymlinkedHomeFilesDirFD(t *testing.T) {
	f, home, work := newTestTrasher(t)
	outside := filepath.Join(work, "outside")
	secret := filepath.Join(outside, "secret.txt")
	writeFile(t, secret, "secret", 0o644)
	if err := os.MkdirAll(filepath.Join(home, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "files")); err != nil {
		t.Fatal(err)
	}
	rec := forgedRecord(filepath.Join(home, "files", "secret.txt"), filepath.Join(work, "stolen.txt"))
	if err := f.Restore(context.Background(), rec); err == nil {
		t.Fatal("restore through a symlinked home files directory was accepted")
	}
	if !exists(secret) {
		t.Error("the out-of-scope file was moved")
	}
}

func TestRestoreRefusesMisplacedTrashRootFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	mount := filepath.Join(work, "mnt")
	fakeMount(f, mount)
	uid := itoa(f.uid)
	tests := []struct {
		name string
		root string
	}{
		{"not a mount point", filepath.Join(work, "evil", ".Trash-"+uid)},
		{"below the mount point", filepath.Join(mount, "sub", ".Trash-"+uid)},
		{"other uid", filepath.Join(mount, ".Trash-"+itoa(f.uid+1))},
		{"other uid in shared trash", filepath.Join(mount, ".Trash", itoa(f.uid+1))},
		{"shared trash without sticky bit", filepath.Join(mount, ".Trash", uid)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			victim := filepath.Join(tt.root, "files", "secret.txt")
			writeFile(t, victim, "secret", 0o644)
			if err := os.MkdirAll(filepath.Join(tt.root, "info"), 0o700); err != nil {
				t.Fatal(err)
			}
			rec := forgedRecord(victim, filepath.Join(work, "repo", "stolen.txt"))
			if err := f.Restore(context.Background(), rec); err == nil {
				t.Fatal("restore from a misplaced trash root was accepted")
			}
			if !exists(victim) || exists(rec.OriginalPath) {
				t.Error("the file was moved")
			}
		})
	}
}

func TestRestoreRefusesUnsafeTopdirTrashFD(t *testing.T) {
	// Each case starts from a genuine trash created by Remove and then
	// degrades it the way another user could have.
	tests := []struct {
		name   string
		mutate func(t *testing.T, f *freedesktop, root string)
	}{
		{"foreign owner", func(_ *testing.T, f *freedesktop, _ string) {
			f.ownerOf = func(fs.FileInfo) (int, bool) { return f.uid + 1, true }
		}},
		{"group and world accessible", func(t *testing.T, _ *freedesktop, root string) {
			if err := os.Chmod(root, 0o777); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, work := newTestTrasher(t)
			mount := filepath.Join(work, "mnt")
			fakeMount(f, mount)
			p := filepath.Join(mount, "f")
			writeFile(t, p, "x", 0o644)
			rec, err := f.Remove(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, f, filepath.Dir(filepath.Dir(rec.StoredPath)))
			if err := f.Restore(context.Background(), rec); err == nil {
				t.Fatal("restore from an unsafe trash was accepted")
			}
			if !exists(rec.StoredPath) || exists(p) {
				t.Error("refused restore moved the item")
			}
		})
	}
}

func TestRemoveSkipsUnsafeTopdirTrashFD(t *testing.T) {
	uid := func(f *freedesktop) string { return ".Trash-" + itoa(f.uid) }
	tests := []struct {
		name  string
		plant func(t *testing.T, f *freedesktop, mount, elsewhere string)
	}{
		{"symlinked files dir", func(t *testing.T, f *freedesktop, mount, elsewhere string) {
			root := filepath.Join(mount, uid(f))
			if err := os.MkdirAll(filepath.Join(root, "info"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, filepath.Join(root, "files")); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlinked info dir", func(t *testing.T, f *freedesktop, mount, elsewhere string) {
			root := filepath.Join(mount, uid(f))
			if err := os.MkdirAll(filepath.Join(root, "files"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, filepath.Join(root, "info")); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlinked trash dir", func(t *testing.T, f *freedesktop, mount, elsewhere string) {
			if err := os.Symlink(elsewhere, filepath.Join(mount, uid(f))); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign owner", func(t *testing.T, f *freedesktop, mount, _ string) {
			if err := os.MkdirAll(filepath.Join(mount, uid(f)), 0o700); err != nil {
				t.Fatal(err)
			}
			f.ownerOf = func(fs.FileInfo) (int, bool) { return f.uid + 1, true }
		}},
		{"open permissions", func(t *testing.T, f *freedesktop, mount, _ string) {
			root := filepath.Join(mount, uid(f))
			if err := os.MkdirAll(filepath.Join(root, "files"), 0o777); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(root, "files"), 0o777); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, home, work := newTestTrasher(t)
			mount := filepath.Join(work, "mnt")
			fakeMount(f, mount)
			elsewhere := filepath.Join(work, "elsewhere")
			if err := os.MkdirAll(elsewhere, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(mount, 0o755); err != nil {
				t.Fatal(err)
			}
			tt.plant(t, f, mount, elsewhere)
			p := filepath.Join(mount, "f")
			writeFile(t, p, "x", 0o644)
			rec, err := f.Remove(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(rec.StoredPath, home) {
				t.Errorf("stored %q, want the home trash %q", rec.StoredPath, home)
			}
			if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
				t.Errorf("wrote through a planted symlink: %v", entries)
			}
		})
	}
}

func TestEnsureTrashDirDoesNotFollowSymlinksFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	dir := filepath.Join(work, "t")
	elsewhere := filepath.Join(work, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "files")); err != nil {
		t.Fatal(err)
	}
	if err := f.ensureTrashDir(dir, true); err == nil {
		t.Fatal("a symlinked files/ was accepted")
	}
	if fi, _ := os.Stat(elsewhere); fi.Mode().Perm() != 0o755 {
		t.Errorf("the link target was modified: %v", fi.Mode())
	}
}

func TestEnsureTrashDirAcceptsOwnExistingDirFD(t *testing.T) {
	f, _, work := newTestTrasher(t)
	dir := filepath.Join(work, "t")
	for i := 0; i < 2; i++ {
		if err := f.ensureTrashDir(dir, true); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
}
