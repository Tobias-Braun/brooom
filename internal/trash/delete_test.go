package trash

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

func newTestDeleter(t *testing.T) Trasher {
	t.Helper()
	tr, err := New(config.StrategyDelete, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestDeleteRemovesItems(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "file"), "12345", 0o644)
	writeFile(t, filepath.Join(root, "tree", "a", "b"), "123", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		size  int64
		isDir bool
	}{
		{"file", 5, false},
		{"tree", 3, true},
		{"empty", 0, true},
	}
	tr := newTestDeleter(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(root, tt.name)
			rec, err := tr.Remove(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if exists(p) {
				t.Error("path still exists")
			}
			if rec.Strategy != config.StrategyDelete || rec.Restorable || rec.StoredPath != "" ||
				rec.OriginalPath != p || rec.SizeBytes != tt.size || rec.IsDir != tt.isDir ||
				rec.RemovedAt.IsZero() || rec.RemovedAt.Location() != time.UTC {
				t.Errorf("unexpected record %+v", rec)
			}
			if err := tr.Restore(context.Background(), rec); !errors.Is(err, ErrNotRestorable) {
				t.Errorf("Restore err = %v, want ErrNotRestorable", err)
			}
		})
	}
}

func TestDeleteNeverFollowsLinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "target", "keep.txt"), "keep", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(root, "target"), filepath.Join(root, "tree", "inner"))
	symlinkOrSkip(t, filepath.Join(root, "target"), filepath.Join(root, "toplink"))
	tr := newTestDeleter(t)

	rec, err := tr.Remove(context.Background(), filepath.Join(root, "toplink"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.IsDir {
		t.Error("symlink record must not be IsDir")
	}
	if _, err := tr.Remove(context.Background(), filepath.Join(root, "tree")); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(root, "target", "keep.txt")) {
		t.Fatal("link target was deleted")
	}
	if exists(filepath.Join(root, "toplink")) || exists(filepath.Join(root, "tree")) {
		t.Error("links or tree remain")
	}
}

func TestDeleteReadOnlyTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("exercising the Windows read-only retry")
	}
	root := t.TempDir()
	p := filepath.Join(root, "tree")
	writeFile(t, filepath.Join(p, "ro.txt"), "x", 0o444)
	writeFile(t, filepath.Join(p, "sub", "ro2.txt"), "y", 0o444)
	if _, err := newTestDeleter(t).Remove(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if exists(p) {
		t.Error("read-only tree survived")
	}
}

func TestRemoveRefusals(t *testing.T) {
	root := t.TempDir()
	quarantineDir := filepath.Join(root, "q")
	if err := os.MkdirAll(filepath.Join(quarantineDir, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	q, err := New(config.StrategyQuarantine, Options{SessionID: "s1", QuarantineDir: quarantineDir})
	if err != nil {
		t.Fatal(err)
	}
	fsRoot := filepath.Clean(string(filepath.Separator))
	if v := filepath.VolumeName(root); v != "" {
		fsRoot = v + string(filepath.Separator)
	}
	tests := []struct {
		name    string
		path    string
		notExit bool
	}{
		{"empty", "", false},
		{"relative", "some/relative", false},
		{"root", fsRoot, false},
		{"missing", filepath.Join(root, "missing"), true},
		{"inside quarantine", filepath.Join(quarantineDir, "s1"), false},
		{"quarantine dir itself", quarantineDir, false},
	}
	for _, tr := range []Trasher{newTestDeleter(t), q} {
		for _, tt := range tests {
			t.Run(string(tr.Strategy())+"/"+tt.name, func(t *testing.T) {
				if tr.Strategy() == config.StrategyDelete && tt.name == "inside quarantine" {
					t.Skip("only the quarantine trasher guards its directory")
				}
				if tr.Strategy() == config.StrategyDelete && tt.name == "quarantine dir itself" {
					t.Skip("only the quarantine trasher guards its directory")
				}
				_, err := tr.Remove(context.Background(), tt.path)
				if err == nil {
					t.Fatal("expected refusal")
				}
				if got := errors.Is(err, fs.ErrNotExist); got != tt.notExit {
					t.Errorf("errors.Is(ErrNotExist) = %v, want %v (err %v)", got, tt.notExit, err)
				}
			})
		}
	}
	if !exists(quarantineDir) {
		t.Fatal("quarantine dir was removed")
	}
}
