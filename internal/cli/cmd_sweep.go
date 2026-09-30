package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/presets"
)

func newSweepCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "sweep [preset]",
		Short: "Scan, show what to clean, ask once, then clean",
		Example: `  brooom sweep
  brooom sweep after-agents
  brooom sweep tidy --dry-run
  brooom sweep everything --yes`,
		Long: sweepLong(),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			p, err := a.resolvePreset(name)
			if err != nil {
				return err
			}
			if err := a.checkPresetDetectors(p); err != nil {
				return err
			}
			return a.runCleanup(cmd, cleanupSelection{
				detectors:     p.Detectors,
				label:         "sweep " + p.Name,
				configOverlay: func(c *config.Config) { *c = *presets.Apply(c, p) },
				keep:          func(f findings.Finding) bool { return p.Keeps(f.Detector, f.Confidence) },
				compact:       true,
			}, af)
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}

// sweepLong renders the help text from the preset data, so the list of
// presets and their detectors cannot drift from what sweep does.
func sweepLong() string {
	var b strings.Builder
	b.WriteString(`Scan the repository, show what would be cleaned, ask once and then clean.
Answer y to clean everything listed; anything else changes nothing. --yes
skips the question (for scripts), --dry-run only shows the plan.

Presets:
`)
	for _, name := range presets.Names() {
		for _, line := range strings.Split(presets.Describe(name), "\n") {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, `Without a preset the config key sweep.preset decides (default %q).
--detector narrows the preset's detectors; it cannot add ones the preset
does not include. Findings below the preset's confidence floor are dropped.

Sweep never removes unmerged or uncommitted work: worktrees with changes,
branches that are not merged and other findings with blocking risk flags are
listed as skipped. Stale branches and large untracked files are in no preset;
use 'brooom scan -d stale-branch' or '-d large-untracked' to list them.

Removed files go to the trash and everything is recorded for 'brooom undo'.`, config.DefaultPreset)
	return b.String()
}

// resolvePreset picks the preset: the positional argument, else sweep.preset
// from the config, else the built-in default. An unknown name is a usage error
// that lists the valid ones. A legacy name (safe, standard, aggressive) runs
// everything, with a note on stderr saying so.
func (a *app) resolvePreset(arg string) (presets.Preset, error) {
	name := arg
	if name == "" {
		cfg, _, err := a.loadConfig()
		if err != nil {
			return presets.Preset{}, err
		}
		name = cfg.Sweep.Preset
	}
	if name == "" {
		name = config.DefaultPreset
	}
	p, legacy, err := presets.Resolve(name)
	if err != nil {
		return presets.Preset{}, usageError{err}
	}
	if legacy && !a.flags.quiet {
		fmt.Fprintf(a.io.Err, "note: the preset %q was renamed; running %q (valid presets: %s)\n",
			output.Sanitize(name), p.Name, strings.Join(presets.Names(), ", "))
	}
	return p, nil
}

// checkPresetDetectors rejects --detector names that the preset does not run
// and says which preset would.
func (a *app) checkPresetDetectors(p presets.Preset) error {
	flagged, err := validateDetectorNames(a.flags.detectors)
	if err != nil {
		return err
	}
	for _, n := range flagged {
		if p.Runs(n) {
			continue
		}
		if other := presets.WithDetector(n); other != "" {
			return usageError{fmt.Errorf("--detector %s is not part of the %s preset; the %s preset includes it", n, p.Name, other)}
		}
		return usageError{fmt.Errorf("--detector %s is in no sweep preset; list its findings with `brooom scan -d %s`", n, n)}
	}
	return nil
}
