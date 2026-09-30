package output

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize makes s safe to print on a terminal or in a line-oriented log by
// replacing every control rune with a visible escape: \n, \r and \t for the
// common ones, \xNN for other C0 controls (ESC becomes \x1b), DEL and C1
// controls, and \uNNNN for the Unicode line and paragraph separators. Bytes
// that are not valid UTF-8 become \xNN as well, because some terminals read a
// stray byte as a C1 control.
//
// Paths, branch names, reasons and error messages come from the filesystem,
// git and other tools, so an attacker controlling a file or branch name could
// otherwise inject terminal escape sequences (a violation of the "no ANSI
// without color" rule) or forge whole output lines with a newline. A backslash
// is deliberately not escaped: it is an ordinary path character on Windows and
// doubling it would make every path there unreadable. The trade-off is that a
// literal "\n" in a name is indistinguishable from an escaped newline, which
// is harmless for display. Strings without control runes are returned as is
// without allocating.
func Sanitize(s string) string {
	if !needsSanitize(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case isUnsafeRune(r):
			b.WriteString(escapeRune(r))
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// needsSanitize is the allocation-free fast path of Sanitize.
func needsSanitize(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || isUnsafeRune(r) {
			return true
		}
		i += size
	}
	return false
}

// isUnsafeRune reports control runes plus the Unicode line and paragraph
// separators, which some terminals and log viewers treat as line breaks.
func isUnsafeRune(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

func escapeRune(r rune) string {
	switch r {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	}
	if r < 0x100 {
		return fmt.Sprintf(`\x%02x`, r)
	}
	return fmt.Sprintf(`\u%04x`, r)
}
