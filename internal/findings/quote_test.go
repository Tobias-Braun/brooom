package findings

import (
	"runtime"
	"testing"
)

// TestQuoteForWindowsWorksInBothShells reproduces #182 item 12: single quotes
// are a literal character in cmd.exe, so a value that needs quoting on Windows
// is double-quoted, which both cmd.exe and PowerShell group into one word. Only
// values that a double quote would leave live (PowerShell expands `$` and the
// backtick, cmd expands %VAR%) fall back to PowerShell single quotes.
func TestQuoteForWindowsWorksInBothShells(t *testing.T) {
	tests := []struct{ in, want string }{
		{`C:\repo\dist`, `C:\repo\dist`},
		{"feat/x", "feat/x"},
		{`C:\my proj\dist`, `"C:\my proj\dist"`},
		{"a&b", `"a&b"`},
		{"a;b", `"a;b"`},
		{"", `""`},
		{"a,b", `"a,b"`},
		{"@x", `"@x"`},
		{"a@b", `"a@b"`},
		{"a+b", `"a+b"`},
		{`C:\my proj\`, `'C:\my proj\'`},
		{"a$b c", `'a$b c'`},
		{"50% done", `'50% done'`},
		{`say "hi"`, `'say "hi"'`},
		{"it's here", `'it''s here'`},
	}
	for _, tt := range tests {
		if got := QuoteFor("windows", tt.in); got != tt.want {
			t.Errorf("QuoteFor(windows, %q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestQuoteForUnixIsShellQuote(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		for _, in := range []string{"a b", "it's", "", "x;y", "plain/path"} {
			if got, want := QuoteFor(goos, in), ShellQuote(in); got != want {
				t.Errorf("QuoteFor(%s, %q) = %q, want %q", goos, in, got, want)
			}
		}
	}
}

func TestQuoteUsesHostOS(t *testing.T) {
	if got, want := Quote("a b"), QuoteFor(runtime.GOOS, "a b"); got != want {
		t.Errorf("Quote = %q, want %q", got, want)
	}
}
