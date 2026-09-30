package cli

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/presets"
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

// invocationArgs returns the arguments of this invocation without the flags a
// hint must not repeat. --apply is dropped so the hint can add exactly one;
// --yes/-y is dropped because a pasted hint would otherwise skip the
// confirmation the user never asked to skip; --format/-f is dropped because an
// explicit machine format is rejected together with --apply, and the hint
// executes rather than reports. Every other flag is kept: dropping one
// (`branches --merged`, `--trash-strategy delete`, `--from file`) would change
// what the suggested command does. Without recorded arguments (a command tree
// that was not started through execute) the command path is the best
// available answer.
func (a *app) invocationArgs(cmd *cobra.Command) []string {
	if len(a.args) == 0 {
		return strings.Fields(cmd.CommandPath())[1:]
	}
	out := make([]string, 0, len(a.args))
	for i := 0; i < len(a.args); i++ {
		arg := a.args[i]
		switch {
		case arg == "--apply", strings.HasPrefix(arg, "--apply="),
			arg == "--yes", strings.HasPrefix(arg, "--yes="),
			strings.HasPrefix(arg, "--format="):
		case arg == "--format":
			i++ // its value is the next argument
		case shortCluster.MatchString(arg):
			kept, consumeNext := stripShortCluster(arg)
			if kept != "" {
				out = append(out, kept)
			}
			if consumeNext {
				i++
			}
		default:
			out = append(out, arg)
		}
	}
	return out
}

// shortCluster matches one or more single-letter flags in one argument (`-y`,
// `-qy`, `-fjson`).
var shortCluster = regexp.MustCompile(`^-[a-zA-Z]+$`)

// stripShortCluster removes -y and -f from a cluster of short flags and
// returns what is left ("" when nothing is). -f takes a value: the rest of the
// cluster when it is attached (`-fjson`), else the next argument, which the
// caller must skip (consumeNext). -d and -p take values as well; from them on
// the cluster is kept as is.
//
// Worked example: `-dlogs` is -d with the attached value "logs". The loop hits
// 'd' first and keeps the rest of the cluster verbatim, so it returns
// "-dlogs"; scanning on would misread 'l', 'o', 'g', 's' as flags. By
// contrast `-qyd` returns "-qd" (y dropped, d kept) and `-yfjson` returns ""
// (both dropped, "json" is f's attached value).
func stripShortCluster(arg string) (kept string, consumeNext bool) {
	var b strings.Builder
	letters := arg[1:]
	for i, r := range letters {
		switch r {
		case 'y':
		case 'f':
			return keepShort(b.String()), i == len(letters)-1
		case 'd', 'p':
			b.WriteString(letters[i:])
			return keepShort(b.String()), false
		default:
			b.WriteRune(r)
		}
	}
	return keepShort(b.String()), false
}

func keepShort(letters string) string {
	if letters == "" {
		return ""
	}
	return "-" + letters
}

// applyCommand is the command that executes what a dry run of cmd showed: the
// same invocation plus --apply. Commands that cannot apply themselves (scan
// and the bare command) get the closest command that can.
//
// Callers that word a hint (rerunHint, purge) only pass commands that have
// --apply, and those always yield a single command. Only scan and the bare
// command can yield the two-step form, and only applyHint words it, as
// separate steps: joining them with `&&` would not run in Windows PowerShell
// 5.1. String() joins the steps with `&&` for display in tests only.
func (a *app) applyCommand(cmd *cobra.Command) string {
	return a.applyPlanFor(cmd).String()
}

// applyPlan is the suggested way to act on a dry run: one command, or, when
// steps is set, the two-step save-then-clean form that must be worded as
// separate steps.
type applyPlan struct {
	command string
	steps   []string
}

// String renders the plan on one line; steps are joined with `&&`.
func (p applyPlan) String() string {
	if p.steps != nil {
		return strings.Join(p.steps, " && ")
	}
	return p.command
}

func (a *app) applyPlanFor(cmd *cobra.Command) applyPlan {
	if cmd.Flags().Lookup("apply") == nil {
		return a.scanApplyPlan(cmd)
	}
	parts := []string{"brooom"}
	for _, arg := range a.invocationArgs(cmd) {
		parts = append(parts, a.quote(arg))
	}
	return applyPlan{command: strings.Join(append(parts, "--apply"), " ")}
}

// scanApplyCommand suggests how to act on what `brooom scan` (or the bare
// command) reported, keeping the scope flags of the invocation. Without
// --detector that is sweep. With --detector it is the shortcut that owns all
// the named detectors; for any other selection sweep could refuse or widen
// the detectors, so the findings are saved to a file and given to
// `clean --from`, which acts on exactly what was reported (see
// fileApplySteps).
//
// --force changes which findings are reported and planned, so a `scan --force`
// hint repeats it on every command that selects or acts.
func (a *app) scanApplyPlan(cmd *cobra.Command) applyPlan {
	scopeFlags := a.scopeFlags()
	detectors := a.detectorFlag()
	force := forceFlag(cmd)
	if len(detectors) == 0 {
		return applyPlan{command: joinCommand("brooom", "sweep", scopeFlags, force, "--apply")}
	}
	if sc := shortcutFor(a.flags.detectors); sc != "" {
		return applyPlan{command: joinCommand("brooom", sc, scopeFlags, detectors, force, "--apply")}
	}
	return applyPlan{steps: a.fileApplySteps(cmd, detectors)}
}

// findingsFile is the file the two-step hint saves the findings to.
const findingsFile = "brooom-findings.json"

// fileApplySteps is the two-step form of acting on a scan: save the findings
// to a file, then clean from that file. It is not a pipe (`scan | clean --from
// -`): the pipe occupies stdin, so clean could neither prompt for the
// confirmation nor pass its terminal check and would refuse to apply. A file
// also gives the user the chance to review or trim the findings before
// applying. Values are quoted for the host's shell (a.quote). The steps stay
// separate strings because `&&` does not exist in Windows PowerShell 5.1;
// applyHint words them as "run A, review the file, then run B".
func (a *app) fileApplySteps(cmd *cobra.Command, detectors []string) []string {
	scopeFlags := a.scopeFlags()
	force := forceFlag(cmd)
	file := a.quote(findingsFile)
	return []string{
		joinCommand("brooom", "scan", scopeFlags, detectors, force, "--format json >", file),
		joinCommand("brooom", "clean", scopeFlags, "--from", file, force, "--apply"),
	}
}

// forceFlag returns --force when cmd has that flag and it is set.
func forceFlag(cmd *cobra.Command) []string {
	if f := cmd.Flags().Lookup("force"); f != nil && f.Value.String() == "true" {
		return []string{"--force"}
	}
	return nil
}

// scopeFlags are the flags that decide where the invocation scans and which
// config it uses; a follow-up command has to repeat them to see the same
// findings.
func (a *app) scopeFlags() []string {
	var out []string
	if a.flags.configPath != "" {
		out = append(out, "--config", a.quote(a.flags.configPath))
	}
	if a.flags.workspaces {
		out = append(out, "--workspaces")
	}
	for _, r := range a.flags.roots {
		out = append(out, "--root", a.quote(r))
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
	return []string{"--detector", a.quote(strings.Join(names, ","))}
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
		return "save the findings to a file and re-run with '--from <file> --apply' (a pipe would take over stdin and leave no terminal to confirm on)"
	}
	return "re-run '" + a.applyCommand(cmd) + "'"
}

// fromStdin reports whether cmd reads its findings file from stdin.
func fromStdin(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("from")
	return f != nil && f.Value.String() == stdinSource
}

// applyHint is the single wording of "nothing was changed, here is how to act
// on it". For a scan it looks at what was reported and names only what would
// really act on those findings (see sweepHint).
func (a *app) applyHint(cmd *cobra.Command, res *scanResult) string {
	plan := a.applyPlanFor(cmd)
	c := plan.command
	if cmd.Flags().Lookup("apply") != nil {
		return "nothing was changed; run `" + c + "` or `brooom sweep`"
	}
	if plan.steps != nil {
		return "nothing was changed; " + fileStepsText(plan.steps)
	}
	if strings.HasPrefix(c, "brooom sweep ") {
		return a.sweepHint(cmd, c, res)
	}
	return "nothing was changed; run `" + c + "`"
}

// fileSteps words fileApplySteps as one sentence fragment.
func (a *app) fileSteps(cmd *cobra.Command, detectors []string) string {
	return fileStepsText(a.fileApplySteps(cmd, detectors))
}

// fileStepsText words the two steps as "run A, review the file, then run B".
// It notes where the file goes: the current directory, replacing a file of
// that name, so it should not be somewhere the scan covers or holds something
// worth keeping.
func fileStepsText(steps []string) string {
	return "run `" + steps[0] + "`, review the file, then run `" + steps[1] + "` (" + findingsFile +
		" is written to the current directory and replaces an existing file of that name)"
}

// sweepHint words the hint of a scan without --detector. `sweep` runs its
// preset, not everything the scan listed: the safe preset only plans
// high-confidence findings of four detectors, so a scan full of medium
// findings would end in "Nothing to sweep". The hint therefore names sweep
// only when the configured preset covers at least one reported finding (and
// says how many when it is not all of them), and otherwise, or for the rest,
// the file-based clean, which acts on exactly what was listed. Shortcut
// commands are named only when their detectors produced findings, and with
// the scope flags of this invocation, or they would fail or scan elsewhere.
// Coverage is judged by detector and confidence; the preset's config overlay
// (for example switched-off log categories) can still narrow it further.
func (a *app) sweepHint(cmd *cobra.Command, sweep string, res *scanResult) string {
	var reported []findings.Finding
	var cfg *config.Config
	if res != nil {
		cfg = res.Config
		if res.Report != nil {
			reported = actionableFindings(res.Report.Findings)
		}
	}
	file := a.fileSteps(cmd, nil)
	p, known := presetOf(cfg)
	covered := 0
	if known {
		covered = countCovered(p, reported)
	}
	if covered == 0 {
		name := "configured"
		if known {
			name = p.Name
		}
		return "nothing was changed; the " + name + " preset would not act on these findings, " + file
	}
	hint := "nothing was changed; run `" + sweep + "`"
	if covered < len(reported) {
		hint += fmt.Sprintf(" (the %s preset covers up to %d of %d actionable findings; to act on all of them, %s)",
			p.Name, covered, len(reported), file)
	}
	if names := a.shortcutHints(reported); len(names) > 0 {
		hint += " or a specific command such as " + strings.Join(names, " or ")
	}
	return hint
}

// actionableFindings keeps the findings that have a suggested action.
func actionableFindings(fs []findings.Finding) []findings.Finding {
	var out []findings.Finding
	for _, f := range fs {
		if f.Actionable() {
			out = append(out, f)
		}
	}
	return out
}

// presetOf resolves the preset a bare `sweep` would run: sweep.preset from
// the config, else the built-in default. known is false for an unknown name,
// where sweep itself would fail.
func presetOf(cfg *config.Config) (p presets.Preset, known bool) {
	name := config.DefaultPreset
	if cfg != nil && cfg.Sweep.Preset != "" {
		name = cfg.Sweep.Preset
	}
	p, err := presets.Get(name)
	return p, err == nil
}

// countCovered counts the findings whose detector the preset runs and whose
// confidence reaches the preset's floor.
func countCovered(p presets.Preset, fs []findings.Finding) int {
	n := 0
	for _, f := range fs {
		if p.Runs(f.Detector) && f.Confidence.Rank() >= p.MinConfidence.Rank() {
			n++
		}
	}
	return n
}

// shortcutHints renders `brooom <shortcut> --apply` for every shortcut whose
// detectors produced one of the findings, with this invocation's scope flags.
func (a *app) shortcutHints(fs []findings.Finding) []string {
	var out []string
	for _, sc := range shortcutDetectors {
		if slices.ContainsFunc(fs, func(f findings.Finding) bool { return slices.Contains(sc.detectors, f.Detector) }) {
			out = append(out, "`"+joinCommand("brooom", sc.command, a.scopeFlags(), "--apply")+"`")
		}
	}
	return out
}
