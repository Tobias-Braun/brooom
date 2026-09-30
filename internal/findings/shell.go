package findings

import (
	"runtime"
	"strings"
	"unicode"
)

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

// Quote quotes s for the shell of the host OS: POSIX on unix, and a form that
// cmd.exe and PowerShell both accept on Windows. It is the one helper every
// suggested and displayed command uses, so a command never mixes dialects.
func Quote(s string) string { return QuoteFor(runtime.GOOS, s) }

// QuoteFor is Quote for an explicit OS, so both dialects are testable on every
// platform. On Windows a safe word stays bare and one that needs quoting gets
// double quotes, because single quotes are a literal character in cmd.exe.
// Double quotes are only used where neither shell can act on the content:
// PowerShell expands `$` and the backtick inside them, cmd.exe expands `%`,
// and a quote or a trailing backslash cannot be represented (the backslash
// would escape the closing quote when the argument is split). Those values
// use PowerShell single quotes, which is the only dialect that can hold them.
func QuoteFor(goos, s string) string {
	if goos != "windows" {
		return ShellQuote(s)
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return r == '%' || !shellSafeRune(r) && r != '\\' }) < 0 {
		return s
	}
	if strings.ContainsAny(s, "\"$`%!^'‘’‚‛") || strings.HasSuffix(s, `\`) || hasControl(s) {
		return powershellQuote(s)
	}
	return `"` + s + `"`
}

// hasControl reports whether s holds a control character, which no quoting
// makes safe to paste on one line.
func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// powershellQuote is a PowerShell single-quoted string: the quote character is
// doubled, and so are the typographic single quotes PowerShell also treats as
// quotes.
func powershellQuote(s string) string {
	r := strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")
	return "'" + r.Replace(s) + "'"
}

// shellSafeRune reports whether r needs no quoting in a POSIX shell word.
func shellSafeRune(r rune) bool {
	alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	return alnum || strings.ContainsRune("/._-:+@%,", r)
}
