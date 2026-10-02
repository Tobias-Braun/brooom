package action

import (
	"runtime"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// This file renders the display-only shell equivalents shown in plans.
// Brooom never runs them; they exist so a user can see (and copy) what a
// step amounts to. They follow the host shell: POSIX on unix, PowerShell on
// Windows.

// displayCommand is the display-only equivalent of the step on this OS.
func displayCommand(path string) string {
	return displayCommandFor(runtime.GOOS, path)
}

// displayCommandFor renders the command for goos; it is separate from
// displayCommand so both dialects are testable on every OS. The Recycle Bin
// has no PowerShell cmdlet, so the Windows variant is a comment (pasting it
// does nothing) that says so and shows the closest cmdlet, marked
// illustrative.
func displayCommandFor(goos, path string) string {
	if goos == "windows" {
		return "# illustrative, Brooom sends it to the Recycle Bin: Remove-Item -LiteralPath " + psQuote(path) + " -Recurse"
	}
	return "trash " + findings.ShellQuote(path)
}

// psQuote quotes s as a PowerShell single-quoted string: the quote character
// is doubled, and so are the typographic single quotes PowerShell also treats
// as quotes.
func psQuote(s string) string {
	r := strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")
	return "'" + r.Replace(s) + "'"
}

// displayQuote quotes a path for the host shell.
func displayQuote(s string) string {
	if runtime.GOOS == "windows" {
		return psQuote(s)
	}
	return findings.Quote(s)
}

// chainCommands joins two display commands so the second only runs when the
// first succeeded (`&&` is not available in Windows PowerShell 5).
func chainCommands(first, second string) string {
	if runtime.GOOS == "windows" {
		return first + "; if ($?) { " + second + " }"
	}
	return first + " && " + second
}
