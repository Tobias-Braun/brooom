package config

import (
	"fmt"
	"path"
	"strings"
)

// BuildDirSpec is one parsed entry of detectors.build-artifacts.dirs or
// extra_dirs.
type BuildDirSpec struct {
	// Name is the directory name or glob on the base name.
	Name string
	// Markers are the sibling files that must exist next to the directory;
	// empty means the directory is reported without any marker.
	Markers []string
}

// ParseBuildDir parses the syntax of the build-artifacts dirs and extra_dirs
// lists: "name" reports every directory of that name (no marker required,
// which is why it must be written out explicitly), and "name:marker1,marker2"
// only reports it next to at least one of the markers. Names and markers may
// be globs on a base name ("*.egg-info", "*.csproj"). The parse is purely
// syntactic; config validation and the detector share it so they cannot
// disagree about what is valid.
func ParseBuildDir(spec string) (BuildDirSpec, error) {
	name, list, gated := strings.Cut(spec, ":")
	if err := checkBuildDirName(name); err != nil {
		return BuildDirSpec{}, fmt.Errorf("%q: %w", spec, err)
	}
	out := BuildDirSpec{Name: name}
	if !gated {
		return out, nil
	}
	for _, m := range strings.Split(list, ",") {
		if err := checkBuildMarker(m); err != nil {
			return BuildDirSpec{}, fmt.Errorf("%q: %w", spec, err)
		}
		out.Markers = append(out.Markers, m)
	}
	return out, nil
}

// checkBuildDirName accepts a name, a glob or "parent/name". Absolute paths,
// "..", empty segments and backslashes are refused so that a configured name
// can never address anything outside the scanned tree.
func checkBuildDirName(name string) error {
	if name == "" {
		return fmt.Errorf("directory name must not be empty")
	}
	segs := strings.Split(name, "/")
	if len(segs) > 2 {
		return fmt.Errorf("directory name may have at most two segments")
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.Contains(s, `\`) {
			return fmt.Errorf("directory name has an empty, relative or backslash segment")
		}
		if _, err := path.Match(s, ""); err != nil {
			return fmt.Errorf("invalid glob: %w", err)
		}
	}
	return nil
}

// checkBuildMarker requires a plain file name or glob without separators.
func checkBuildMarker(m string) error {
	if m == "" || m == "." || m == ".." || strings.ContainsAny(m, `/\`) {
		return fmt.Errorf("marker %q must be a file name or glob without separators", m)
	}
	if _, err := path.Match(m, ""); err != nil {
		return fmt.Errorf("marker %q: %w", m, err)
	}
	return nil
}

// validateBuildDirs reports every invalid dirs or extra_dirs entry.
func validateBuildDirs(p *problemList, field string, list []string) {
	for i, s := range list {
		if _, err := ParseBuildDir(s); err != nil {
			p.add(fmt.Sprintf("%s[%d]", field, i), "%v", err)
		}
	}
}
