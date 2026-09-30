package walk

import (
	"path/filepath"
	"testing"
)

func TestIsVCSName(t *testing.T) {
	for name, want := range map[string]bool{
		".git": true, ".hg": true, ".jj": true, ".svn": true,
		".github": false, ".gitignore": false, "git": false, "": false,
	} {
		if got := IsVCSName(name); got != want {
			t.Errorf("IsVCSName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDirShape(t *testing.T) {
	tests := []struct {
		name string
		add  func(s *DirShape)
		want bool
	}{
		{"complete", func(s *DirShape) {
			s.Add("HEAD", false, true)
			s.Add("objects", true, false)
			s.Add("refs", true, false)
		}, true},
		{"HEAD as directory", func(s *DirShape) {
			s.Add("HEAD", true, false)
			s.Add("objects", true, false)
			s.Add("refs", true, false)
		}, false},
		{"objects as symlink", func(s *DirShape) {
			s.Add("HEAD", false, true)
			s.Add("objects", false, false)
			s.Add("refs", true, false)
		}, false},
		{"missing refs", func(s *DirShape) {
			s.Add("HEAD", false, true)
			s.Add("objects", true, false)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s DirShape
			tt.add(&s)
			if s.IsBareRepo() != tt.want {
				t.Errorf("IsBareRepo = %v, want %v", s.IsBareRepo(), tt.want)
			}
		})
	}
}

func TestHasVCSEntry(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  bool
	}{
		{"empty", func(*testing.T, string) {}, false},
		{"git", func(t *testing.T, d string) { writeFile(t, filepath.Join(d, ".git"), 1) }, true},
		{"hg", func(t *testing.T, d string) { writeFile(t, filepath.Join(d, ".hg", "x"), 1) }, true},
		{"jj", func(t *testing.T, d string) { writeFile(t, filepath.Join(d, ".jj", "x"), 1) }, true},
		{"svn", func(t *testing.T, d string) { writeFile(t, filepath.Join(d, ".svn", "x"), 1) }, true},
		{"bare", func(t *testing.T, d string) { makeBare(t, d) }, true},
		{"refs only", func(t *testing.T, d string) { writeFile(t, filepath.Join(d, "refs", "x"), 1) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			if got := HasVCSEntry(dir); got != tt.want {
				t.Errorf("HasVCSEntry = %v, want %v", got, tt.want)
			}
		})
	}
}
