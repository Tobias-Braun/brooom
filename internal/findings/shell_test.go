package findings

import "testing"

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/a/b-c_d.txt", "/a/b-c_d.txt"},
		{"feat/x", "feat/x"},
		{"/a b/c", "'/a b/c'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
		{"fix;touch${IFS}pwned", "'fix;touch${IFS}pwned'"},
		{"a$(id)`id`", "'a$(id)`id`'"},
		{"a&b|c", "'a&b|c'"},
		{"line\nbreak", "'line\nbreak'"},
	}
	for _, tt := range tests {
		if got := ShellQuote(tt.in); got != tt.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
