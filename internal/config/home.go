package config

import (
	"errors"
	"fmt"
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

// EnsureDirs creates the Brooom home and its cache, sessions and quarantine
// subdirectories with mode 0700 (they can hold paths and file contents of the
// user's projects) and returns the layout. Existing directories are left
// untouched, not chmod-ed, so a user's own permissions are respected; an
// existing non-directory is an error naming it.
func EnsureDirs() (Dirs, error) {
	d, err := ResolveDirs()
	if err != nil {
		return Dirs{}, err
	}
	for _, p := range []string{d.Home, d.Cache, d.Sessions, d.Quarantine} {
		if err := ensureDir(p); err != nil {
			return Dirs{}, err
		}
	}
	return d, nil
}

func ensureDir(p string) error {
	fi, err := os.Stat(p)
	switch {
	case err == nil && !fi.IsDir():
		return fmt.Errorf("%s exists but is not a directory", p)
	case err == nil:
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("stat %s: %w", p, err)
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", p, err)
	}
	return nil
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
