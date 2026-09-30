package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/presets"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// Dynamic shell completions.
//
// Every function here runs while the user presses TAB, so it must stay
// instant and silent: it only reads registries, the config file and the
// session manifests (never scans, prompts, writes or uses the network), and
// every failure degrades to an empty candidate list instead of an error. All
// of them answer ShellCompDirectiveNoFileComp, because a file name is never
// a valid value for these flags.

// completionDirective is returned by every dynamic completion.
const completionDirective = cobra.ShellCompDirectiveNoFileComp

// registerCompletions wires the completion functions of flag values and
// positional arguments into the tree. It walks the tree instead of being
// called from each command file so that a command only has to declare a flag
// with a well-known name to get its completion.
func registerCompletions(root *cobra.Command, a *app) {
	// Registration only fails for a missing flag or a duplicate, both
	// programming errors that the completion tests catch, so the errors are
	// deliberately not surfaced at runtime.
	_ = root.RegisterFlagCompletionFunc("detector", completeDetectors)
	_ = root.RegisterFlagCompletionFunc("format", completeFormats)
	_ = root.RegisterFlagCompletionFunc("progress", completeProgressModes)
	_ = root.RegisterFlagCompletionFunc("root", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return a.completeRoots(cmd, toComplete, true)
	})

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c != root {
			registerLocalCompletions(c)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)

	registerSessionArgs(root, a)
}

// registerLocalCompletions registers the completions of flags that commands
// declare themselves (not inherited from the root).
func registerLocalCompletions(c *cobra.Command) {
	local := c.LocalNonPersistentFlags()
	if local.Lookup("trash-strategy") != nil {
		_ = c.RegisterFlagCompletionFunc("trash-strategy", completeTrashStrategies)
	}
	if local.Lookup("preset") != nil {
		_ = c.RegisterFlagCompletionFunc("preset", completePresets)
	}
}

// registerSessionArgs completes session ids for `undo` and `sessions` and the
// configured roots for `roots remove`.
func registerSessionArgs(root *cobra.Command, a *app) {
	sessionIDs := func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, completionDirective
		}
		return a.completeSessionIDs(toComplete), completionDirective
	}
	for _, path := range [][]string{{"undo"}, {"sessions"}} {
		if c, _, err := root.Find(path); err == nil && c != root {
			c.ValidArgsFunction = sessionIDs
		}
	}
	if c, _, err := root.Find([]string{"roots", "remove"}); err == nil && c.Name() == "remove" {
		c.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return a.completeRoots(cmd, toComplete, false)
		}
	}
}

// candidate formats one completion entry; cobra shows the text after the tab
// as the description in shells that support it.
func candidate(value, description string) string {
	if description == "" {
		return value
	}
	return value + "\t" + description
}

// splitCommaPrefix splits the word being completed at its last comma so
// list-valued flags such as --detector a,b complete the segment after the
// comma. prefix keeps everything up to and including the comma and chosen
// holds the values already given.
func splitCommaPrefix(toComplete string) (prefix, current string, chosen []string) {
	i := strings.LastIndex(toComplete, ",")
	if i < 0 {
		return "", toComplete, nil
	}
	prefix, current = toComplete[:i+1], toComplete[i+1:]
	for _, v := range strings.Split(prefix, ",") {
		if v != "" {
			chosen = append(chosen, v)
		}
	}
	return prefix, current, chosen
}

// completeDetectors offers detector names with their descriptions. The
// registry is preferred because it knows what this build really contains;
// the static name list is the fallback for builds without linked detectors.
func completeDetectors(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	prefix, current, chosen := splitCommaPrefix(toComplete)
	descriptions := map[string]string{}
	var names []string
	for _, d := range detect.All() {
		names = append(names, d.Name())
		descriptions[d.Name()] = d.Description()
	}
	if len(names) == 0 {
		names = config.DetectorNames()
	}
	var out []string
	for _, n := range names {
		if slices.Contains(chosen, n) || !strings.HasPrefix(n, current) {
			continue
		}
		out = append(out, candidate(prefix+n, descriptions[n]))
	}
	return out, completionDirective
}

// completeFormats offers the registered output formats.
func completeFormats(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return filterPrefix(output.Names(), toComplete, nil), completionDirective
}

// completeProgressModes offers the values of --progress.
func completeProgressModes(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	descriptions := map[string]string{
		progressAuto:   "draw on stderr only for an interactive terminal (default)",
		progressAlways: "always draw, also without a terminal",
		progressNever:  "never draw",
	}
	return filterPrefix(progressModes, toComplete, descriptions), completionDirective
}

// presetNames is the single place the --preset completion gets its values
// from.
func presetNames() []string { return presets.Names() }

// completePresets offers the sweep presets with their one-line summaries.
func completePresets(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	descriptions := map[string]string{}
	for _, n := range presetNames() {
		if p, err := presets.Get(n); err == nil {
			descriptions[n] = p.Summary
		}
	}
	return filterPrefix(presetNames(), toComplete, descriptions), completionDirective
}

// completeTrashStrategies offers the trash strategies; the description of
// delete says loudly that it cannot be undone.
func completeTrashStrategies(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	descriptions := map[string]string{
		string(config.StrategyTrash):      "move to the OS trash (default)",
		string(config.StrategyQuarantine): "move to ~/.brooom/quarantine, kept for a retention period",
		string(config.StrategyDelete):     "delete permanently (cannot be undone)",
	}
	names := []string{string(config.StrategyTrash), string(config.StrategyQuarantine), string(config.StrategyDelete)}
	return filterPrefix(names, toComplete, descriptions), completionDirective
}

// filterPrefix returns the names starting with prefix, annotated with their
// descriptions (which may be nil).
func filterPrefix(names []string, prefix string, descriptions map[string]string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, candidate(n, descriptions[n]))
		}
	}
	return out
}

// completeRoots offers the roots stored in the config, already listed ones
// excluded when completing a repeated argument. A config that cannot be
// loaded yields nothing. Only a list-valued flag (--root) is split at commas;
// a positional argument is one path, and a comma is a legal path character.
func (a *app) completeRoots(_ *cobra.Command, toComplete string, listValued bool) ([]string, cobra.ShellCompDirective) {
	prefix, current, chosen := "", toComplete, []string(nil)
	if listValued {
		prefix, current, chosen = splitCommaPrefix(toComplete)
	}
	var out []string
	for _, r := range a.configuredRootStrings() {
		if slices.Contains(chosen, r) || !strings.HasPrefix(r, current) {
			continue
		}
		out = append(out, prefix+r)
	}
	return out, completionDirective
}

// completeSessionIDs lists the recorded session ids, newest first, with the
// start time and a summary as description. Unreadable manifests and store
// errors are ignored: completion must never fail.
func (a *app) completeSessionIDs(toComplete string) []string {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return nil
	}
	list, _, err := session.NewStore(dirs.Sessions).List()
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range list {
		if strings.HasPrefix(m.ID, toComplete) {
			out = append(out, candidate(m.ID, sessionSummary(m)))
		}
	}
	return out
}

// sessionSummary is the description shown next to a session id, e.g.
// "2026-09-29 22:45 sweep --apply, 3 applied, 1.0 MiB".
func sessionSummary(m *session.Manifest) string {
	c := m.Counts()
	// The command line is user data; a tab or newline in it would corrupt the
	// value/description protocol.
	command := strings.Join(strings.Fields(m.Command), " ")
	return fmt.Sprintf("%s %s, %d applied, %s",
		m.StartedAt.Local().Format("2006-01-02 15:04"), command, c.Applied, output.FormatSize(m.ReclaimedBytes))
}
