package catalog

import "testing"

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		fold          bool
		want          bool
	}{
		{"*.log", "a.log", false, true},
		{"*.log", "a.txt", false, false},
		{"*.log", "dir/a.log", false, false},
		{"a/*", "a/b/c", false, false},
		{"a?c", "abc", false, true},
		{"a?c", "a/c", false, false},
		{"a?c", "ac", false, false},
		{"[abc].txt", "b.txt", false, true},
		{"[abc].txt", "d.txt", false, false},
		{"[a-c]x", "bx", false, true},
		{"[a-c]x", "dx", false, false},
		{"[!x]y", "ay", false, true},
		{"[!x]y", "xy", false, false},
		{"[]a]", "]", false, true},
		{"**", "a/b/c", false, true},
		{"**/x.log", "x.log", false, true},
		{"**/x.log", "a/b/x.log", false, true},
		{"**/x.log", "a/b/y.log", false, false},
		{"a/**/b", "a/b", false, true},
		{"a/**/b", "a/x/y/b", false, true},
		{"a/**/b", "a/x/y/c", false, false},
		{"a/**", "a", false, true},
		{"a/**", "a/b/c", false, true},
		{"a/**", "b/a", false, false},
		{"a/**/**/b", "a/b", false, true},
		{"a/*/b", "a/x/b", false, true},
		{"a/*/b", "a/b", false, false},
		{".claude/*.log", ".claude/run.log", false, true},
		{"*.LOG", "a.log", false, false},
		{"*.LOG", "a.log", true, true},
		{"[A-Z]x", "bx", true, true},
		{"Dir/File", "dir/FILE", true, true},
		{"Dir/File", "dir/FILE", false, false},
		{"a", "", false, false},
		{"*", "", false, false},
		{"[a", "a", false, false},
		{"a*b*c", "aXbYc", false, true},
		{"a*b*c", "aXbY", false, false},
	}
	for _, tt := range tests {
		if got := GlobMatch(tt.pattern, tt.name, tt.fold); got != tt.want {
			t.Errorf("GlobMatch(%q, %q, fold=%v) = %v, want %v", tt.pattern, tt.name, tt.fold, got, tt.want)
		}
	}
}

func TestValidateGlob(t *testing.T) {
	tests := []struct {
		pattern string
		ok      bool
	}{
		{"*.log", true},
		{"a/**/b", true},
		{"**/x", true},
		{"a/**", true},
		{"[a-z]*", true},
		{"[!x]", true},
		{"[]a]", true},
		{"", false},
		{"a/", false},
		{"/a", false},
		{"a//b", false},
		{"a**b", false},
		{"**.log", false},
		{"a/**b/c", false},
		{"[abc", false},
		{"a[", false},
		{"[]", false},
		{"[z-a]", false},
	}
	for _, tt := range tests {
		err := ValidateGlob(tt.pattern)
		if (err == nil) != tt.ok {
			t.Errorf("ValidateGlob(%q) = %v, want ok=%v", tt.pattern, err, tt.ok)
		}
	}
}
