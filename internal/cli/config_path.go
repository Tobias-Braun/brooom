package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/output"
)

// configPath returns the effective config file path: the --config flag, else
// ~/.brooom/config.json (or $BROOOM_HOME). The file need not exist.
func (a *app) configPath() (string, error) {
	if a.flags.configPath != "" {
		return a.flags.configPath, nil
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return "", err
	}
	return dirs.ConfigFile, nil
}

// loadConfigForEdit returns the effective config path and the configuration
// loaded from it (defaults when the file is missing).
func loadConfigForEdit(a *app) (string, *config.Config, error) {
	path, err := a.configPath()
	if err != nil {
		return "", nil, err
	}
	if err := a.requireExplicitConfig(path); err != nil {
		return path, nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return path, nil, err
	}
	return path, cfg, nil
}

// requireExplicitConfig fails when --config names a file that does not exist.
// A typo would otherwise silently fall back to the defaults, while `scan`
// already refuses it. The implicit ~/.brooom/config.json may be absent.
func (a *app) requireExplicitConfig(path string) error {
	if a.flags.configPath == "" {
		return nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("config file not found: %s", output.Sanitize(path))
	}
	return nil
}
