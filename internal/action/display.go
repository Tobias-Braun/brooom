package action

import (
	"runtime"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// This file renders the display-only shell equivalents shown in plans.
// Brooom never runs them; they exist so a user can see (and copy) what a
// step amounts to. They follow the host shell: POSIX on unix, PowerShell on
// Windows.

// quarantineTail is where the quarantine trasher really puts an item: below
// the session directory, in a numbered directory of its own. The session id
// is only generated when the run starts, so the plan shows placeholders.
const quarantineTail = "<session-id>/<n>/"

// displayCommand is the display-only equivalent of the step on this OS.
func displayCommand(strategy config.TrashStrategy, path string) string {
	return displayCommandFor(runtime.GOOS, strategy, path, quarantineDir())
}

// quarantineDir is the configured quarantine directory, or a placeholder when
// the Brooom home cannot be resolved.
func quarantineDir() string {
	if d, err := config.ResolveDirs(); err == nil {
		return d.Quarantine
	}
	return "<quarantine dir>"
}

// displayCommandFor renders the command for goos; it is separate from
// displayCommand so both dialects are testable on every OS.
func displayCommandFor(goos string, strategy config.TrashStrategy, path, qdir string) string {
	if goos == "windows" {
		return powershellCommand(strategy, path, qdir)
	}
	q := findings.ShellQuote(path)
	switch strategy {
	case config.StrategyDelete:
		return "rm -rf -- " + q
	case config.StrategyQuarantine:
		return "mv -- " + q + " " + findings.ShellQuote(strings.TrimRight(qdir, "/")+"/"+quarantineTail)
	default:
		return "trash " + q
	}
}

// powershellCommand is the PowerShell dialect. The Recycle Bin has no
// PowerShell cmdlet, so that variant is a comment (pasting it does nothing)
// that says so and shows the closest cmdlet, marked illustrative.
func powershellCommand(strategy config.TrashStrategy, path, qdir string) string {
	q := psQuote(path)
	switch strategy {
	case config.StrategyDelete:
		return "Remove-Item -LiteralPath " + q + " -Recurse -Force"
	case config.StrategyQuarantine:
		dest := strings.TrimRight(qdir, `\`) + `\` + strings.ReplaceAll(quarantineTail, "/", `\`)
		return "Move-Item -LiteralPath " + q + " -Destination " + psQuote(dest)
	default:
		return "# illustrative, Brooom sends it to the Recycle Bin: Remove-Item -LiteralPath " + q + " -Recurse"
	}
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
	return findings.ShellQuote(s)
}

// chainCommands joins two display commands so the second only runs when the
// first succeeded (`&&` is not available in Windows PowerShell 5).
func chainCommands(first, second string) string {
	if runtime.GOOS == "windows" {
		return first + "; if ($?) { " + second + " }"
	}
	return first + " && " + second
}
