package cli

import (
	"fmt"
	"os"
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
		Use:   "sweep [preset] [path]",
		Short: "Scan, show what to clean, ask once, then clean",
		Example: `  brooom sweep
  brooom sweep after-agents
  brooom sweep tidy ~/code --dry-run
  brooom sweep everything --yes`,
		Long: sweepLong(),
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, path, err := splitSweepArgs(args)
			if err != nil {
				return err
			}
			a.flags.path = path
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
	b.WriteString(`Scan the repository (or the repository or folder the path names; below a
folder every repository and project is swept), show what would be cleaned, ask
once and then clean.
Answer y to clean everything listed; anything else changes nothing. On a
terminal, e opens a list of every item to untick what should stay (space
toggles, a toggles a group, enter cleans the checked items, q changes
nothing). --yes skips the question (for scripts), --dry-run only shows the
plan.

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
listed as skipped; 'brooom review' decides on them one by one.

--dry-run, or a machine format (json, ndjson, plain), prints the report in
the chosen --format and changes nothing. The plain format is a bare path list
for pipes and omits informational findings, such as linked worktrees outside
the scanned scope; use another format to see them.

Removed files go to the OS trash and everything is recorded for 'brooom undo'.`, config.DefaultPreset)
	return b.String()
}

// splitSweepArgs reads `[preset] [path]`. With two arguments the first is the
// preset. A single argument is a preset when it names one (legacy names
// included) and a path otherwise, so `brooom sweep ~/code` works; `./tidy`
// sweeps a folder named tidy.
func splitSweepArgs(args []string) (preset, path string, err error) {
	switch len(args) {
	case 0:
		return "", "", nil
	case 2:
		if _, _, err := presets.Resolve(args[0]); err != nil {
			return "", "", usageError{err}
		}
		return args[0], args[1], nil
	}
	if _, _, err := presets.Resolve(args[0]); err == nil {
		return args[0], "", nil
	}
	if looksLikePath(args[0]) {
		return "", args[0], nil
	}
	if fi, err := os.Stat(args[0]); err == nil && fi.IsDir() {
		return "", args[0], nil
	}
	_, _, err = presets.Resolve(args[0])
	return "", "", usageError{err}
}

// resolvePreset picks the preset: the positional argument, else sweep.preset
// from the config, else the built-in default. An unknown name is a usage error
// that lists the valid ones. A legacy name (safe, standard, aggressive) runs
// everything, with a note on stderr saying so.
func (a *app) resolvePreset(arg string) (presets.Preset, error) {
	name := arg
	if name == "" {
		cfg, err := a.loadConfig()
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
		return usageError{fmt.Errorf("--detector %s is in no sweep preset; `brooom review` decides on its findings", n)}
	}
	return nil
}
