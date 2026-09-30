package trash

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestRestoreRefusesForeignTrashLocations covers manifests forged so the
// stored path merely contains a component named .Trash or .Trashes.
func TestRestoreRefusesForeignTrashLocations(t *testing.T) {
	home := t.TempDir()
	m := newMacTrash(home)
	m.uid = 501
	work := t.TempDir()
	tests := []struct {
		name   string
		stored string
	}{
		{"any dir named .Trash", filepath.Join(work, "evil", ".Trash", "secret")},
		{"any dir named .Trashes", filepath.Join(work, "evil", ".Trashes", "501", "secret")},
		{"nested below ~/.Trash", filepath.Join(home, ".Trash", "sub", "secret")},
		{"volume trashes of another uid", "/Volumes/Data/.Trashes/502/secret"},
		{"volume trashes without uid dir", "/Volumes/Data/.Trashes/secret"},
		{"trashes below a nested volume path", "/Volumes/Data/x/.Trashes/501/secret"},
		{"unclean path", home + "/.Trash/../.Trash/secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Only plant the file where the test owns the tree; the
			// /Volumes cases must never create anything on the real disk.
			if filepath.Clean(tt.stored) == tt.stored && (isWithin(work, tt.stored) || isWithin(home, tt.stored)) {
				writeFile(t, tt.stored, "secret", 0o644)
			}
			rec := Record{OriginalPath: filepath.Join(work, "repo", "stolen"), StoredPath: tt.stored, Restorable: true}
			if err := m.Restore(context.Background(), rec); err == nil {
				t.Fatal("restore was accepted")
			}
			if exists(rec.OriginalPath) {
				t.Error("the file was moved")
			}
		})
	}
}

func TestRestoreRefusesSymlinkedTrashDir(t *testing.T) {
	home := t.TempDir()
	m := newMacTrash(home)
	outside := filepath.Join(t.TempDir(), "outside")
	writeFile(t, filepath.Join(outside, "secret"), "secret", 0o644)
	if err := os.Symlink(outside, filepath.Join(home, ".Trash")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rec := Record{
		OriginalPath: filepath.Join(t.TempDir(), "stolen"),
		StoredPath:   filepath.Join(home, ".Trash", "secret"),
		Restorable:   true,
	}
	if err := m.Restore(context.Background(), rec); err == nil {
		t.Fatal("restore through a symlinked ~/.Trash was accepted")
	}
	if !exists(filepath.Join(outside, "secret")) || exists(rec.OriginalPath) {
		t.Error("the out-of-scope file was moved")
	}
}

// The file carries no build tag on purpose: the code under test compiles on
// every OS, and only this path-shape test skips itself on Windows.
func TestIsTrashRootMac(t *testing.T) {
	// The inputs are macOS paths with forward slashes; isTrashRoot compares
	// them with filepath, which uses backslashes on Windows.
	if runtime.GOOS == "windows" {
		t.Skip("macOS path shapes")
	}
	m := newMacTrash("/Users/me")
	m.uid = 501
	tests := []struct {
		root string
		want bool
	}{
		{"/Users/me/.Trash", true},
		{"/.Trashes/501", true},
		{"/Volumes/Data/.Trashes/501", true},
		{"/Users/me/.Trashes/501", false},
		{"/Volumes/Data/.Trashes/" + strconv.Itoa(502), false},
		{"/Volumes/Data/.Trash", false},
		{"/Users/other/.Trash", false},
		{"/tmp/.Trash", false},
	}
	for _, tt := range tests {
		if got := m.isTrashRoot(tt.root); got != tt.want {
			t.Errorf("isTrashRoot(%q) = %v, want %v", tt.root, got, tt.want)
		}
	}
}
