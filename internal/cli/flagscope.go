package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// scopeCommands are the commands that build a scan scope and select
// detectors, and that render findings; they are the only ones for which -w,
// --root and -d mean anything and all of them read --format. `undo` builds a
// scope too but has no detectors and no report, see scopeOnlyCommands.
var scopeCommands = map[string]bool{
	"brooom": true, "brooom scan": true, "brooom sweep": true,
	"brooom clean": true, "brooom git purge": true,
}

// noFormatCommands are scopeCommands that never render in a chosen format:
// `clean` prints its verdict and plan as text whatever --format says, so an
// explicit --format (a script asking for JSON) would be silently ignored.
// Only the flag is refused; output.format from the config is not, because it
// is a default for the commands that do read it.
var noFormatCommands = map[string]bool{"brooom clean": true}

// scopeOnlyCommands build a scope (-w, --root) but neither select detectors nor
// render findings: `undo` restores what a manifest names, so -d and -f would
// be accepted and ignored.
var scopeOnlyCommands = map[string]bool{"brooom undo": true}

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
	if !ignoresScanFlag(path, flag) {
		return ""
	}
	return fmt.Sprintf("--%s has no effect on '%s' and is not supported there", flag, strings.TrimPrefix(path, "brooom "))
}

// ignoresScanFlag reports whether the command at path never reads flag.
func ignoresScanFlag(path, flag string) bool {
	scopeOnly, ok := scanOnlyFlags[flag]
	switch {
	case !ok:
		return false
	case flag == "format" && noFormatCommands[path]:
		return true
	case scopeCommands[path], !scopeOnly && formatCommands[path]:
		return false
	}
	return !scopeOnlyCommands[path] || (flag != "workspaces" && flag != "root")
}

// rejectIgnoredScanFlags runs before every command. Completion and help are
// exempt because cobra parses the flags of the words being completed.
func rejectIgnoredScanFlags(cmd *cobra.Command) error {
	if skipsUpdateCheck(cmd) && !formatCommands[cmd.CommandPath()] {
		return nil
	}
	if err := rejectEmptySelectors(cmd); err != nil {
		return err
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

// listSelectorFlags are the list flags that narrow what a command acts on.
// Empty input must never fall back to "everything": a script with
// `--id "$SELECTED" --yes` and an unset variable would otherwise act
// on all findings.
var listSelectorFlags = []string{"id", "root", "detector"}

// rejectEmptySelectors returns a usage error when a selector flag was given
// but names nothing (`--id ""`, `--root ","`) or contains an empty element
// (`a,,b`). pflag splits values at commas, so `--id ""` arrives as an empty
// slice that is only distinguishable from an omitted flag by Changed.
func rejectEmptySelectors(cmd *cobra.Command) error {
	for _, name := range listSelectorFlags {
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		sv, ok := f.Value.(pflag.SliceValue)
		if !ok {
			continue
		}
		values := sv.GetSlice()
		if len(values) == 0 || slices.ContainsFunc(values, func(v string) bool { return strings.TrimSpace(v) == "" }) {
			return usageError{fmt.Errorf("--%s needs at least one non-empty value and no empty list elements; an empty selector would widen the selection to everything", name)}
		}
	}
	return nil
}
