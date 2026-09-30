package scope

import "testing"

func TestExcluded(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		rel      string
		want     bool
	}{
		{"base name any depth", []string{"tmp"}, "a/b/tmp", true},
		{"base name top", []string{"tmp"}, "tmp", true},
		{"base name is not a prefix", []string{"tmp"}, "tmpx", false},
		{"base name matches only last segment", []string{"tmp"}, "tmp/keep", false},
		{"base glob", []string{"build-*"}, "x/build-arm", true},
		{"double star any depth", []string{"**/scratch"}, "a/b/scratch", true},
		{"double star zero segments", []string{"**/scratch"}, "scratch", true},
		{"double star wrong name", []string{"**/scratch"}, "a/scratch2", false},
		{"anchored single star", []string{"a/*/b"}, "a/x/b", true},
		{"anchored is root relative", []string{"a/*/b"}, "z/a/x/b", false},
		{"anchored wrong depth", []string{"a/*/b"}, "a/b", false},
		{"anchored too deep", []string{"a/*/b"}, "a/x/b/c", false},
		{"middle double star", []string{"a/**/c"}, "a/b/x/c", true},
		{"middle double star zero", []string{"a/**/c"}, "a/c", true},
		{"trailing slash ignored", []string{"tmp/"}, "x/tmp", true},
		{"invalid pattern ignored", []string{"[", "a/[/b"}, "a/x/b", false},
		{"invalid does not block valid", []string{"[", "tmp"}, "tmp", true},
		{"root never excluded", []string{"**", "tmp"}, "", false},
		{"no patterns", nil, "a", false},
		{"empty pattern", []string{""}, "a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Excluded(tt.patterns, tt.rel); got != tt.want {
				t.Fatalf("Excluded(%q, %q) = %v, want %v", tt.patterns, tt.rel, got, tt.want)
			}
		})
	}
}
