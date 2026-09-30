package findings

import "strings"

// ShellQuote returns s as one POSIX shell word: unchanged when it only holds
// characters that are safe unquoted, else single-quoted with embedded single
// quotes closed, escaped and reopened. Suggested commands are display-only and never run
// through a shell by Brooom, but users copy and paste them, and refnames and
// paths may hold ';', '$', '`', '&', '|', parentheses, braces or spaces, so
// every untrusted value in a Command must pass through here.
func ShellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !shellSafeRune(r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSafeRune reports whether r needs no quoting in a POSIX shell word.
func shellSafeRune(r rune) bool {
	alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	return alnum || strings.ContainsRune("/._-:+@%", r)
}
