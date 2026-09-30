package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// configEnv isolates a test from the real home: BROOOM_HOME, HOME and
// USERPROFILE all point into temp dirs. It returns the config file path, a
// scratch workspace dir and the fake home.
func configEnv(t *testing.T) (cfgPath, work, home string) {
	t.Helper()
	brooom := t.TempDir()
	home = t.TempDir()
	work = t.TempDir()
	t.Setenv(config.HomeEnv, brooom)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(brooom, config.ConfigFileName), work, home
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
