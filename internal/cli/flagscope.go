package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagReaders lists, per persistent flag that only some commands use, the
// commands that read it: sweep and review select detectors (review narrows
// its fixed pair), and sweep plus the listing commands render in a chosen
// format. review, undo and empty-trash print plans and questions as text.
var flagReaders = map[string]map[string]bool{
	"detector": {"brooom sweep": true, "brooom review": true},
	"format": {
		"brooom sweep": true, "brooom config show": true, "brooom sessions": true, "brooom version": true,
	},
}

// unsupportedScanFlag returns a usage message when flag is a scan-only flag
// that the command at path ignores, and "" when the flag is fine. Accepting
// and silently ignoring a flag misleads: `config show -d nope` would report
// success although no detector was ever consulted.
func unsupportedScanFlag(path, flag string) string {
	if !ignoresScanFlag(path, flag) {
		return ""
	}
	return fmt.Sprintf("--%s has no effect on '%s' and is not supported there", flag, strings.TrimPrefix(path, "brooom "))
}

// ignoresScanFlag reports whether the command at path never reads flag.
func ignoresScanFlag(path, flag string) bool {
	readers, ok := flagReaders[flag]
	return ok && !readers[path]
}

// rejectIgnoredScanFlags runs before every command. Completion and help are
// exempt because cobra parses the flags of the words being completed.
func rejectIgnoredScanFlags(cmd *cobra.Command) error {
	if isCompletionOrHelp(cmd) {
		return nil
	}
	if err := rejectEmptySelectors(cmd); err != nil {
		return err
	}
	for name := range flagReaders {
		if f := cmd.Flags().Lookup(name); f == nil || !f.Changed {
			continue
		}
		if msg := unsupportedScanFlag(cmd.CommandPath(), name); msg != "" {
			return usageError{fmt.Errorf("%s", msg)}
		}
	}
	return nil
}

// isCompletionOrHelp reports whether cmd is part of the completion machinery
// or help, which cobra runs with the flags of the words being completed.
func isCompletionOrHelp(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "completion", "__complete", "__completeNoDesc", "help":
			return true
		}
	}
	return false
}

// listSelectorFlags are the flags that narrow what a command acts on. Empty
// input must never fall back to "everything": a script with
// `--detector "$SELECTED" --yes` and an unset variable would otherwise act on
// every detector.
var listSelectorFlags = []string{"detector", "path"}

// rejectEmptySelectors returns a usage error when a selector flag was given
// but names nothing (`--detector ""`, `--detector ","`, `--path ""`) or
// contains an empty element (`a,,b`), or a single-valued selector is blank.
// pflag splits values at commas, so `--detector ""` arrives as an empty slice
// that is only distinguishable from an omitted flag by Changed.
func rejectEmptySelectors(cmd *cobra.Command) error {
	for _, name := range listSelectorFlags {
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		sv, ok := f.Value.(pflag.SliceValue)
		if !ok {
			// A single-valued selector (--path): blank means "not given",
			// which would silently fall back to the working directory.
			if strings.TrimSpace(f.Value.String()) == "" {
				return usageError{fmt.Errorf("--%s needs a non-empty value", name)}
			}
			continue
		}
		values := sv.GetSlice()
		if len(values) == 0 || slices.ContainsFunc(values, func(v string) bool { return strings.TrimSpace(v) == "" }) {
			return usageError{fmt.Errorf("--%s needs at least one non-empty value and no empty list elements; an empty selector would widen the selection to everything", name)}
		}
	}
	return nil
}
