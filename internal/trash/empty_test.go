package trash

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

func TestInOSTrash(t *testing.T) {
	for p, want := range map[string]bool{
		"/Users/me/.Trash/app":                           true,
		"/Volumes/Data/.Trashes/501/app":                 true,
		"/home/me/.local/share/Trash/files/app":          true,
		"/home/me/.local/share/Trash/info/app.trashinfo": true,
		"/mnt/d/.Trash-1000/files/app":                   true,
		"/mnt/d/.Trash/1000/files/app":                   true,
		`C:\$Recycle.Bin\S-1-5-21\$RABC123`:              true,
		"/Users/me/.Trash":                               false,
		"/home/me/.local/share/Trash/files":              false,
		"/Users/me/code/app":                             false,
		"/home/me/Trashy/files/app":                      false,
		"/home/me/code/files/app":                        false,
	} {
		if got := InOSTrash(p); got != want {
			t.Errorf("InOSTrash(%q) = %v, want %v", p, got, want)
		}
	}
}

// trashItem writes a file into a fake macOS trash and returns its record.
func trashItem(t *testing.T) (Record, string) {
	t.Helper()
	trashDir := filepath.Join(t.TempDir(), ".Trash")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(trashDir, "app.log")
	if err := os.WriteFile(p, []byte("log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return Record{Strategy: config.StrategyTrash, StoredPath: p, OriginalPath: "/code/app.log", SizeBytes: walk.AllocatedSize(fi)}, trashDir
}

func TestRemoveStoredDeletesOnlyVerifiedItems(t *testing.T) {
	r, trashDir := trashItem(t)
	if err := RemoveStored(r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(r.StoredPath); !os.IsNotExist(err) {
		t.Errorf("stored copy still there: %v", err)
	}
	if _, err := os.Stat(trashDir); err != nil {
		t.Errorf("the trash directory itself must stay: %v", err)
	}
}

func TestVerifyStoredRefusals(t *testing.T) {
	r, _ := trashItem(t)
	outside := r
	outside.StoredPath = filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(outside.StoredPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	quarantine := r
	quarantine.Strategy = config.StrategyQuarantine
	changedType := r
	changedType.IsDir = true
	changedSize := r
	changedSize.SizeBytes = r.SizeBytes + 1
	gone := r
	gone.StoredPath = filepath.Join(filepath.Dir(r.StoredPath), "gone")
	for name, tc := range map[string]struct {
		r    Record
		want error
	}{
		"outside the trash": {outside, ErrNotInOSTrash},
		"quarantine record": {quarantine, ErrNotInOSTrash},
		"type changed":      {changedType, ErrStoredChanged},
		"size changed":      {changedSize, ErrStoredChanged},
		"already gone":      {gone, os.ErrNotExist},
	} {
		if err := RemoveStored(tc.r); !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", name, err, tc.want)
		}
	}
	if _, err := os.Lstat(outside.StoredPath); err != nil {
		t.Error("a file outside the trash was deleted")
	}
	if _, err := os.Lstat(r.StoredPath); err != nil {
		t.Error("a refused record deleted the stored copy")
	}
}

func TestRemoveStoredDropsTrashInfo(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "Trash", "files")
	info := filepath.Join(root, "Trash", "info")
	for _, d := range []string{files, info} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	item := filepath.Join(files, "dist")
	if err := os.MkdirAll(filepath.Join(item, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(info, "dist.trashinfo")
	if err := os.WriteFile(meta, []byte("[Trash Info]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Record{Strategy: config.StrategyTrash, StoredPath: item, InfoPath: meta, IsDir: true}
	if err := RemoveStored(r); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{item, meta} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
	}
}
