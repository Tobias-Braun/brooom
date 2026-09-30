package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/presets"
)

func newSweepCmd(a *app) *cobra.Command {
	var af applyFlags
	var preset string
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "The no-brainer: scan and clean with a preset",
		Example: `  brooom sweep
  brooom sweep --preset standard --apply
  brooom sweep --workspaces --root ~/code --detector build-artifacts`,
		Long: sweepLong(),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.resolvePreset(cmd, preset)
			if err != nil {
				return err
			}
			if err := a.checkPresetDetectors(p); err != nil {
				return err
			}
			return a.runCleanup(cmd, cleanupSelection{
				detectors:       p.Detectors,
				label:           "sweep",
				configOverlay:   func(c *config.Config) { *c = *presets.Apply(c, p) },
				minConfidence:   p.MinConfidence,
				skipUnavailable: true,
			}, af)
		},
	}
	cmd.Flags().StringVarP(&preset, "preset", "p", "",
		fmt.Sprintf("preset: %s (default: sweep.preset from the config, else %s)",
			strings.Join(presets.Names(), ", "), config.DefaultPreset))
	addApplyFlags(cmd, &af)
	return cmd
}

// sweepLong renders the help text from the preset data, so the list of
// presets and their detectors cannot drift from what sweep does.
func sweepLong() string {
	var b strings.Builder
	b.WriteString("Scan with a preset and clean up what it finds.\n\nPresets:\n")
	for _, name := range presets.Names() {
		for _, line := range strings.Split(presets.Describe(name), "\n") {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(`Without --preset the config key sweep.preset decides (default "safe").
--detector narrows the preset's detectors; it cannot add ones the preset
does not include. Findings below the preset's minimum confidence are dropped.
Findings with blocking risk flags are shown as blocked and not planned; only
an explicit --force lifts them, exactly as in every other command. Presets
never change the trash strategy, protected branches or the confirmation.

Like every command, sweep is a dry run unless you pass --apply.`)
	return b.String()
}

// resolvePreset picks the preset: the --preset flag, else sweep.preset from
// the config, else the built-in default. An unknown name is a usage error that
// lists the valid ones. The config is only read when the flag is absent.
func (a *app) resolvePreset(cmd *cobra.Command, flagValue string) (presets.Preset, error) {
	name := flagValue
	if !cmd.Flags().Changed("preset") {
		cfg, _, err := a.loadConfig()
		if err != nil {
			return presets.Preset{}, err
		}
		name = cfg.Sweep.Preset
	}
	if name == "" {
		name = config.DefaultPreset
	}
	p, err := presets.Get(name)
	if err != nil {
		return presets.Preset{}, usageError{err}
	}
	return p, nil
}

// checkPresetDetectors rejects --detector names that the preset does not run
// and says which preset would. It only checks membership: whether a detector
// is linked into this build is decided later, where an explicitly named
// detector that is missing is an error.
func (a *app) checkPresetDetectors(p presets.Preset) error {
	flagged, err := validateKnownDetectorNames(a.flags.detectors)
	if err != nil {
		return err
	}
	for _, n := range flagged {
		if !p.Runs(n) {
			return usageError{fmt.Errorf("--detector %s is not part of the %s preset; the %s preset includes it",
				n, p.Name, presets.WithDetector(n))}
		}
	}
	return nil
}
