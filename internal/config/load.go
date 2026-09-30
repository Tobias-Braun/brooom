package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

const (
	// maxConfigBytes caps the global config file; a real one is a few KiB, so
	// anything larger is a mistake (or an attempt to exhaust memory).
	maxConfigBytes = 1 << 20
	// maxRepoConfigBytes caps .brooom.json, which is untrusted input.
	maxRepoConfigBytes = 64 << 10
)

// Load reads the config file at path and merges it over Default. A missing
// file is not an error (defaults are returned); any other read error is
// returned wrapped with the path.
//
// The file is decoded strictly: unknown keys, wrong types, trailing data and
// files over 1 MiB are errors that name the offending key path (or line and
// column for syntax errors). An empty file is an error (probably a truncated
// write); "{}" is a valid empty configuration. A UTF-8 BOM is tolerated.
//
// Merge semantics: values are decoded into a pre-populated Default(), so
// fields absent from the file keep their defaults. Slices and maps present in
// the file REPLACE the default value entirely (they are not merged); for
// example git.protected_branches in the file is the complete list, and
// trash.per_detector is not combined with defaults. A JSON null for a slice
// or map yields an empty one.
//
// The loaded configuration is validated; the error lists every problem.
func Load(path string) (*Config, error) {
	data, err := readCapped(path, maxConfigBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(path, data)
}

// Parse decodes and validates config file content exactly like Load does
// after reading the file; label (normally the path) prefixes every error.
// It exists so callers that rewrite the file can check the bytes they are
// about to write with the very same rules as Load, before anything reaches
// the disk.
func Parse(label string, data []byte) (*Config, error) {
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("%s: content is larger than the %d KiB limit", label, maxConfigBytes>>10)
	}
	cfg := Default()
	if err := decodeStrict(label, data, cfg); err != nil {
		return nil, err
	}
	normalizeNulls(cfg)
	noteDeprecated(cfg)
	if cfg.Version < 1 || cfg.Version > CurrentVersion {
		return nil, fmt.Errorf("%s: version: %s", label, versionMessage(cfg.Version, CurrentVersion))
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return cfg, nil
}

// normalizeNulls turns the nil slices produced by an explicit JSON null into
// empty ones, so callers never see a difference between "[]" and "null".
func normalizeNulls(c *Config) {
	if c.Git.ProtectedBranches == nil {
		c.Git.ProtectedBranches = []string{}
	}
	if c.Git.BaseBranches == nil {
		c.Git.BaseBranches = []string{}
	}
}

// noteDeprecated records the keys of earlier releases the file still sets.
// They are accepted so an old file keeps loading, but have no effect.
//
// `config init` wrote an empty "roots" list and user_locations false into
// every file, so those are dropped silently: they never had an effect.
func noteDeprecated(c *Config) {
	for _, ul := range []struct {
		key string
		v   **bool
	}{
		{"detectors.ai-artifacts.user_locations", &c.Detectors.AIArtifacts.LegacyUserLocations},
		{"detectors.log-and-runtime-files.user_locations", &c.Detectors.Logs.LegacyUserLocations},
	} {
		if *ul.v != nil && **ul.v {
			c.Deprecated = append(c.Deprecated, ul.key+": is ignored; agent data of the scanned repositories is always included and global caches are no longer cleaned")
		}
		*ul.v = nil
	}
	roots := bytes.TrimSpace(c.LegacyRoots)
	c.LegacyRoots = nil
	if len(roots) > 0 && !bytes.Equal(roots, []byte("null")) && !bytes.Equal(bytes.Join(bytes.Fields(roots), nil), []byte("[]")) {
		c.Deprecated = append(c.Deprecated, "roots: the root registry was removed and the key is ignored; pass a path instead, e.g. `brooom sweep ~/code`")
	}
}

// readCapped reads a whole regular file of at most limit bytes. The error is
// wrapped with the path; a missing file satisfies errors.Is(err, fs.ErrNotExist).
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	return readCappedFile(f, path, limit)
}

func readCappedFile(f *os.File, path string, limit int64) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("read %s: is a directory", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("read %s: file is larger than the %d KiB limit", path, limit>>10)
	}
	return data, nil
}

// openRegular opens path only if it is a regular file that is not a symlink.
// A missing file yields nil, nil.
func openRegular(path string) (*os.File, error) {
	lfi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if !lfi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: refusing to read repo config that is a symlink or not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// Guard against the path being swapped for a link between Lstat and Open.
	if fi, err := f.Stat(); err != nil || !os.SameFile(lfi, fi) {
		_ = f.Close()
		return nil, fmt.Errorf("%s: changed while being opened; refusing to read it", path)
	}
	return f, nil
}

// RepoConfig is the per-repo .brooom.json. It can only make Brooom more
// careful: disable detectors, raise age/size thresholds, add protected
// branches and excludes. Anything that would loosen a rule is not
// representable here and is therefore rejected by strict decoding with the
// offending key path.
type RepoConfig struct {
	// Version of the file format; 0 (missing) and 1 are accepted.
	Version int `json:"version"`
	// Disable lists detector names to disable for this repo.
	Disable []string `json:"disable,omitempty"`
	// Thresholds may only raise MinAgeDays/MinSizeBytes/RecentDays.
	Thresholds *ThresholdOverrides `json:"thresholds,omitempty"`
	// ProtectedBranches are added to the global list.
	ProtectedBranches []string `json:"protected_branches,omitempty"`
	// Exclude lists glob patterns (relative to the repo root) to skip.
	Exclude []string `json:"exclude,omitempty"`
}

// LoadRepoConfig reads a repository's .brooom.json. A missing file returns
// nil, nil. The file is untrusted (the repository may come from anywhere), so
// it is decoded strictly, limited to 64 KiB, and refused when it is a symlink
// or not a regular file: following a link would let a repository make Brooom
// read arbitrary files. Every error names the file.
func LoadRepoConfig(path string) (*RepoConfig, error) {
	f, err := openRegular(path)
	if err != nil || f == nil {
		return nil, err
	}
	defer f.Close()
	data, err := readCappedFile(f, path, maxRepoConfigBytes)
	if err != nil {
		return nil, err
	}
	rc := &RepoConfig{}
	if err := decodeStrict(path, data, rc); err != nil {
		return nil, err
	}
	if rc.Version < 0 || rc.Version > CurrentVersion {
		return nil, fmt.Errorf("%s: version: %s", path, versionMessage(rc.Version, CurrentVersion))
	}
	return rc, nil
}
