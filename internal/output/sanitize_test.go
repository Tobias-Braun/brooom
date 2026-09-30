package output

import "testing"

func TestSanitize(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "src/main.go", "src/main.go"},
		{"unicode kept", "héllo/日本語", "héllo/日本語"},
		{"windows path kept", `C:\Users\me`, `C:\Users\me`},
		{"newline", "a\nb", `a\nb`},
		{"carriage return", "a\r\nb", `a\r\nb`},
		{"tab", "a\tb", `a\tb`},
		{"escape", "\x1b[31mred", `\x1b[31mred`},
		{"osc bell", "\x1b]0;title\x07", `\x1b]0;title\x07`},
		{"nul and del", "a\x00b\x7f", `a\x00b\x7f`},
		{"c1 control", "a\u009bb", `a\x9bb`},
		{"line separator", "a\u2028b\u2029", `a\u2028b\u2029`},
		{"invalid utf8", "a\x9bb", `a\x9bb`},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.in); got != tt.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
