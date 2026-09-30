package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// scopeCommands are the commands that build a scan scope and select
// detectors, and that render findings; they are the only ones for which -w,
// --root and -d mean anything and all of them read --format.
var scopeCommands = map[string]bool{
	"brooom": true, "brooom scan": true, "brooom sweep": true, "brooom branches": true,
	"brooom worktrees": true, "brooom logs": true, "brooom artifacts": true, "brooom ai": true,
	"brooom clean": true, "brooom git purge": true, "brooom undo": true,
}

// formatCommands are the commands outside scopeCommands that read --format,
// each with its own small set of formats.
var formatCommands = map[string]bool{
	"brooom config show": true, "brooom roots list": true, "brooom sessions": true,
	"brooom version": true, "brooom update-check": true,
}

// scanOnlyFlags lists the persistent flags that only some commands use. The
// value says whether the flag is limited to scopeCommands (true) or is also
// read by formatCommands (false).
var scanOnlyFlags = map[string]bool{
	"workspaces": true, "root": true, "detector": true, "format": false,
}

// unsupportedScanFlag returns a usage message when flag is a scan-only flag
// that the command at path ignores, and "" when the flag is fine. Accepting
// and silently ignoring a flag misleads: `config validate -d nope` used to
// report success although no detector was ever consulted.
func unsupportedScanFlag(path, flag string) string {
	scopeOnly, ok := scanOnlyFlags[flag]
	if !ok || scopeCommands[path] || (!scopeOnly && formatCommands[path]) {
		return ""
	}
	return fmt.Sprintf("--%s has no effect on '%s' and is not supported there", flag, strings.TrimPrefix(path, "brooom "))
}

// rejectIgnoredScanFlags runs before every command. Completion and help are
// exempt because cobra parses the flags of the words being completed.
func rejectIgnoredScanFlags(cmd *cobra.Command) error {
	if skipsUpdateCheck(cmd) && !formatCommands[cmd.CommandPath()] {
		return nil
	}
	for name := range scanOnlyFlags {
		if f := cmd.Flags().Lookup(name); f == nil || !f.Changed {
			continue
		}
		if msg := unsupportedScanFlag(cmd.CommandPath(), name); msg != "" {
			return usageError{fmt.Errorf("%s", msg)}
		}
	}
	return nil
}
