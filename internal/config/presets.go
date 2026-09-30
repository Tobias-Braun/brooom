package config

// DefaultPreset is the sweep preset used when neither the --preset flag nor
// sweep.preset names one.
const DefaultPreset = "safe"

// PresetNames lists the valid values of sweep.preset, least to most
// aggressive. The presets package owns the preset definitions but imports
// this package, so config cannot ask it for the names without an import
// cycle; a test in internal/presets pins this list to presets.Names().
func PresetNames() []string {
	return []string{"safe", "standard", "aggressive"}
}
