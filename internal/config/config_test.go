package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultsEnableEveryDetector(t *testing.T) {
	cfg := Default()
	for _, name := range DetectorNames() {
		if !cfg.DetectorEnabled(name) {
			t.Errorf("detector %q disabled by default", name)
		}
	}
	if cfg.DetectorEnabled("no-such-detector") {
		t.Error("unknown detector reported as enabled")
	}
}

func TestHomeOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(HomeEnv, dir)
	d, err := ResolveDirs()
	if err != nil {
		t.Fatal(err)
	}
	if d.Home != filepath.Clean(dir) || d.ConfigFile != filepath.Join(dir, ConfigFileName) {
		t.Errorf("unexpected dirs %+v", d)
	}
	t.Setenv(HomeEnv, "relative/path")
	if _, err := Home(); err == nil {
		t.Error("relative BROOOM_HOME must be rejected")
	}
}
