package config

// DefaultPreset is the sweep preset used when neither the positional preset
// argument nor sweep.preset names one.
const DefaultPreset = "everything"

// PresetNames lists the valid values of sweep.preset, narrowest first. The
// presets package owns the preset definitions but imports this package, so
// config cannot ask it for the names without an import cycle; a test in
// internal/presets pins this list to presets.Names().
func PresetNames() []string {
	return []string{"after-agents", "tidy", "everything"}
}

// LegacyPresetNames are the preset names of earlier releases. sweep.preset
// still accepts them (they run everything, see presets.Resolve) so an existing
// config file written by `brooom config init` keeps loading.
func LegacyPresetNames() []string {
	return []string{"safe", "standard", "aggressive"}
}
