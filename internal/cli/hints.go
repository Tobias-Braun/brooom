package cli

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// shortcutDetectors maps the shortcut commands to the detectors they own. A
// hint may only suggest a shortcut for --detector values it accepts, because
// a shortcut refuses detectors outside its selection.
var shortcutDetectors = []struct {
	command   string
	detectors []string
}{
	{"branches", []string{config.DetectorStaleBranch, config.DetectorMergedBranch}},
	{"worktrees", []string{config.DetectorWorktrees}},
	{"logs", []string{config.DetectorLogs}},
	{"artifacts", []string{config.DetectorBuildArtifacts}},
	{"ai", []string{config.DetectorAIArtifacts}},
}

// invocationArgs returns the arguments of this invocation without any
// --apply, so a hint can add exactly one. Every other flag is kept: dropping
// one (`branches --merged`, `--trash-strategy delete`, `--from file`) would
// change what the suggested command does. Without recorded arguments (a
// command tree that was not started through execute) the command path is the
// best available answer.
func (a *app) invocationArgs(cmd *cobra.Command) []string {
	if len(a.args) == 0 {
		return strings.Fields(cmd.CommandPath())[1:]
	}
	out := make([]string, 0, len(a.args))
	for _, arg := range a.args {
		if arg == "--apply" || strings.HasPrefix(arg, "--apply=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// applyCommand is the command that executes what a dry run of cmd showed: the
// same invocation plus --apply. Commands that cannot apply themselves (scan
// and the bare command) get the closest command that can.
func (a *app) applyCommand(cmd *cobra.Command) string {
	if cmd.Flags().Lookup("apply") == nil {
		return a.scanApplyCommand()
	}
	parts := []string{"brooom"}
	for _, arg := range a.invocationArgs(cmd) {
		parts = append(parts, quoteArg(arg))
	}
	return strings.Join(append(parts, "--apply"), " ")
}

// scanApplyCommand suggests how to act on what `brooom scan` (or the bare
// command) reported, keeping the scope flags of the invocation. Without
// --detector that is sweep. With --detector it is the shortcut that owns all
// the named detectors; for any other selection sweep could refuse or widen
// the detectors, so the findings are piped through `clean --from -`, which
// acts on exactly what was reported.
func (a *app) scanApplyCommand() string {
	scopeFlags := a.scopeFlags()
	detectors := a.detectorFlag()
	if len(detectors) == 0 {
		return joinCommand("brooom", "sweep", scopeFlags, "--apply")
	}
	if sc := shortcutFor(a.flags.detectors); sc != "" {
		return joinCommand("brooom", sc, scopeFlags, detectors, "--apply")
	}
	scan := joinCommand("brooom", "scan", scopeFlags, detectors, "--format json")
	return scan + " | " + joinCommand("brooom", "clean", scopeFlags, "--from - --apply")
}

// scopeFlags are the flags that decide where the invocation scans and which
// config it uses; a follow-up command has to repeat them to see the same
// findings.
func (a *app) scopeFlags() []string {
	var out []string
	if a.flags.configPath != "" {
		out = append(out, "--config", quoteArg(a.flags.configPath))
	}
	if a.flags.workspaces {
		out = append(out, "--workspaces")
	}
	for _, r := range a.flags.roots {
		out = append(out, "--root", quoteArg(r))
	}
	return out
}

// detectorFlag renders --detector as one flag, or nothing when it was not given.
func (a *app) detectorFlag() []string {
	var names []string
	for _, n := range a.flags.detectors {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []string{"--detector", quoteArg(strings.Join(names, ","))}
}

// shortcutFor returns the shortcut command whose detectors include every
// named detector, or "" when there is none.
func shortcutFor(names []string) string {
	for _, sc := range shortcutDetectors {
		all := true
		for _, n := range names {
			if n = strings.TrimSpace(n); n != "" && !slices.Contains(sc.detectors, n) {
				all = false
				break
			}
		}
		if all {
			return sc.command
		}
	}
	return ""
}

// joinCommand joins words and flag groups into one command line. Groups are
// already quoted.
func joinCommand(parts ...any) string {
	var out []string
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			out = append(out, v)
		case []string:
			out = append(out, v...)
		}
	}
	return strings.Join(out, " ")
}

// rerunHint completes the executor's "dry run: nothing was changed; ... to
// execute" line. A findings file read from stdin is gone after the dry run,
// so repeating the command with --apply would read an empty stdin; the hint
// says how to get the same result instead.
func (a *app) rerunHint(cmd *cobra.Command) string {
	if fromStdin(cmd) {
		return "save the findings to a file and re-run with '--from <file> --apply', or pipe them in again with --apply"
	}
	return "re-run '" + a.applyCommand(cmd) + "'"
}

// fromStdin reports whether cmd reads its findings file from stdin.
func fromStdin(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("from")
	return f != nil && f.Value.String() == stdinSource
}

// applyHint is the single wording of "nothing was changed, here is how to act
// on it".
func (a *app) applyHint(cmd *cobra.Command) string {
	c := a.applyCommand(cmd)
	if cmd.Flags().Lookup("apply") == nil {
		hint := "nothing was changed; run `" + c + "`"
		if strings.HasPrefix(c, "brooom sweep ") {
			hint += " or a specific command such as `brooom branches --apply`"
		}
		return hint
	}
	return "nothing was changed; run `" + c + "` or `brooom sweep`"
}
