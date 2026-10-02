package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/presets"
)

// scanHint is the one line after a scan report with actionable findings: the
// sweep that acts on them. Sweep shows its plan and asks before it changes
// anything, so the hint does not need to spell out a preview step. The
// configured preset is named only when it is not the one a bare `sweep` runs
// anyway; when it covers none of the findings, everything is suggested
// instead. Findings no preset acts on (stale branches, large untracked files)
// are counted, not explained: `brooom help sweep` says why.
//
// Only scan and the bare command print it; other commands that render a scan
// report (git purge, a machine-format sweep) have their own guidance.
func (a *app) scanHint(cmd *cobra.Command, res *scanResult) string {
	if cmd != cmd.Root() && cmd.Name() != "scan" {
		return ""
	}
	var reported []findings.Finding
	var cfg *config.Config
	if res != nil {
		cfg = res.Config
		if res.Report != nil {
			reported = actionableFindings(res.Report.Findings)
		}
	}
	configured := presetOf(cfg)
	p, covered := configured, countCovered(configured, reported)
	if covered == 0 {
		p = mustPreset(presets.Everything)
		covered = countCovered(p, reported)
	}
	if covered == 0 {
		return "nothing was changed; no sweep preset acts on these findings (see `brooom help sweep`)"
	}
	parts := []string{"brooom", "sweep"}
	if p.Name != configured.Name || a.flags.path != "" {
		// A lone path would be read as a preset if it were spelled like one,
		// so the preset is spelled out whenever a path follows.
		parts = append(parts, p.Name)
	}
	if a.flags.path != "" {
		parts = append(parts, a.quote(a.flags.path))
	}
	parts = append(parts, a.configFlag()...)
	parts = append(parts, a.presetDetectorFlag(p)...)
	hint := "nothing was changed; run `" + strings.Join(parts, " ") + "` to review and clean these"
	if covered < len(reported) {
		hint += fmt.Sprintf(" (%d of %d; the rest are in no sweep preset)", covered, len(reported))
	}
	return hint
}

// rerunHint completes the executor's "dry run: nothing was changed; ... to
// execute" line. A findings file read from stdin is gone after the dry run,
// so repeating the command would read an empty stdin; the hint says how to get
// the same result instead.
func (a *app) rerunHint(cmd *cobra.Command) string {
	if fromStdin(cmd) {
		return "save the findings to a file and re-run with '--from <file>' (a pipe would take over stdin and leave no terminal to confirm on)"
	}
	return "re-run without --dry-run"
}

// fromStdin reports whether cmd reads its findings file from stdin.
func fromStdin(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("from")
	return f != nil && f.Value.String() == stdinSource
}

// scopeFlags are the flags that decide where the invocation worked and which
// config it used, spelled for the commands that take the scope as --path
// (undo, clean); a follow-up command has to repeat them to see the same
// findings.
func (a *app) scopeFlags() []string {
	out := a.configFlag()
	if a.flags.path != "" {
		out = append(out, "--path", a.quote(a.flags.path))
	}
	return out
}

// configFlag repeats --config when the invocation named a config file.
func (a *app) configFlag() []string {
	if a.flags.configPath == "" {
		return nil
	}
	return []string{"--config", a.quote(a.flags.configPath)}
}

// presetDetectorFlag renders the --detector names of this invocation that the
// preset runs as one flag, or nothing. Names the preset does not run are
// dropped: sweep would refuse them.
func (a *app) presetDetectorFlag(p presets.Preset) []string {
	var names []string
	for _, n := range a.flags.detectors {
		if n = strings.TrimSpace(n); n != "" && p.Runs(n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []string{"--detector", a.quote(strings.Join(names, ","))}
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
// the config (legacy names included), else the built-in default. An unknown
// name, where sweep itself would fail, counts as the default.
func presetOf(cfg *config.Config) presets.Preset {
	if cfg != nil && cfg.Sweep.Preset != "" {
		if p, _, err := presets.Resolve(cfg.Sweep.Preset); err == nil {
			return p
		}
	}
	return mustPreset(config.DefaultPreset)
}

// mustPreset returns a built-in preset; the names are constants, so a failure
// is a programming error that the preset tests catch.
func mustPreset(name string) presets.Preset {
	p, err := presets.Get(name)
	if err != nil {
		panic(err)
	}
	return p
}

// countCovered counts the findings the preset would plan: its detector runs
// and the confidence reaches the preset's floor for that detector.
func countCovered(p presets.Preset, fs []findings.Finding) int {
	n := 0
	for _, f := range fs {
		if p.Runs(f.Detector) && p.Keeps(f.Detector, f.Confidence) {
			n++
		}
	}
	return n
}
