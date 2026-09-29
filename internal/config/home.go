package config

import (
	"errors"
	"os"
	"path/filepath"
)

// HomeEnv overrides the Brooom home directory (default ~/.brooom). Tests and
// portable setups use it; it must point to an absolute path.
const HomeEnv = "BROOOM_HOME"

// ConfigFileName is the name of the global config file inside the home dir.
const ConfigFileName = "config.json"

// RepoConfigFileName is the per-repo config file that may only tighten rules.
const RepoConfigFileName = ".brooom.json"

// Home returns the Brooom home directory: $BROOOM_HOME if set, otherwise
// ~/.brooom. It does not create the directory.
func Home() (string, error) {
	if h := os.Getenv(HomeEnv); h != "" {
		if !filepath.IsAbs(h) {
			return "", errors.New(HomeEnv + " must be an absolute path")
		}
		return filepath.Clean(h), nil
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(u, ".brooom"), nil
}

// Dirs are the well-known subdirectories of the Brooom home.
type Dirs struct {
	Home       string
	ConfigFile string
	Cache      string
	Sessions   string
	Quarantine string
}

// ResolveDirs returns the Brooom home layout without creating anything.
func ResolveDirs() (Dirs, error) {
	h, err := Home()
	if err != nil {
		return Dirs{}, err
	}
	return Dirs{
		Home:       h,
		ConfigFile: filepath.Join(h, ConfigFileName),
		Cache:      filepath.Join(h, "cache"),
		Sessions:   filepath.Join(h, "sessions"),
		Quarantine: filepath.Join(h, "quarantine"),
	}, nil
}
