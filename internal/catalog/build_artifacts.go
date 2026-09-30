package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// buildArtifactsFile is the single embedded file in the build artifact
// format. It is deliberately not in toolFiles: its schema (directory names
// gated by sibling project markers) has nothing in common with the tool
// format, so each format has its own strict decoder and neither loader ever
// reads the other's file.
const buildArtifactsFile = "build_artifacts.json"

// MarkerMode says how the markers of an entry combine.
type MarkerMode string

// Marker modes. MarkerModeAny is the default: one matching sibling is enough.
// MarkerModeAll exists for entries such as Go's vendor directory, which is
// only a dependency copy when both go.mod and go.sum are present.
const (
	MarkerModeAny MarkerMode = "any"
	MarkerModeAll MarkerMode = "all"
)

// BuildArtifactEntry is one build output or dependency directory of an
// ecosystem, gated by project markers next to it.
type BuildArtifactEntry struct {
	// ID is a unique kebab-case identifier, reported as evidence.
	ID string `json:"id"`
	// Ecosystem names the tool family (node, rust, python, ...); findings
	// carry it as their Tool.
	Ecosystem string `json:"ecosystem"`
	// Dir is a directory name or a glob on the base name such as
	// "*.egg-info". A value with one "/" (".angular/cache") matches a nested
	// directory whose first segment sits next to the markers.
	Dir string `json:"dir"`
	// Markers are file names or globs that must exist in the directory that
	// contains Dir (the parent). Empty means no marker is required.
	Markers []string `json:"markers"`
	// MarkerMode combines Markers; empty means "any".
	MarkerMode MarkerMode `json:"marker_mode"`
	// ConfidenceCap is the highest confidence findings of this entry can
	// get; empty means "high".
	ConfidenceCap Confidence `json:"confidence_cap"`
	// RequireFileInside names a file that must exist inside the matched
	// directory itself (pyvenv.cfg for virtual environments), so that a
	// plain folder called "env" is never mistaken for one.
	RequireFileInside string `json:"require_file_inside,omitempty"`
	// Description says what the directory is and how it regenerates.
	Description string `json:"description"`
}

// buildArtifactsDoc is the on-disk shape of build_artifacts.json.
type buildArtifactsDoc struct {
	SchemaVersion int                  `json:"schema_version"`
	Entries       []BuildArtifactEntry `json:"entries"`
}

// BuildArtifacts returns the validated entries of the embedded
// build_artifacts.json with defaults applied (marker_mode any,
// confidence_cap high). The file is decoded strictly: unknown fields, a
// tools-format file and invalid values are errors.
func BuildArtifacts() ([]BuildArtifactEntry, error) {
	sub, err := fs.Sub(dataFS, "data")
	if err != nil {
		return nil, fmt.Errorf("catalog: embedded data: %w", err)
	}
	return buildArtifactsFrom(sub, buildArtifactsFile)
}

// buildArtifactsFrom is BuildArtifacts with an injectable file system so that
// tests can feed malformed files.
func buildArtifactsFrom(fsys fs.FS, name string) ([]BuildArtifactEntry, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("catalog: %s: %w", name, err)
	}
	var doc buildArtifactsDoc
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("catalog: %s: decode: %w", name, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("catalog: %s: decode: unexpected data after the top-level object", name)
	}
	if doc.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("catalog: %s: unsupported schema_version %d (want %d)", name, doc.SchemaVersion, SchemaVersion)
	}
	pr := &problemList{}
	if len(doc.Entries) == 0 {
		pr.add(name, "needs at least one entry")
	}
	seen := map[string]bool{}
	for i := range doc.Entries {
		e := &doc.Entries[i]
		applyBuildArtifactDefaults(e)
		validateBuildArtifact(pr, fmt.Sprintf("%s: entries[%d] %q", name, i, e.ID), *e)
		if seen[e.ID] {
			pr.add(name, "duplicate entry id %q", e.ID)
		}
		seen[e.ID] = true
	}
	if err := pr.err(); err != nil {
		return nil, err
	}
	return doc.Entries, nil
}

func applyBuildArtifactDefaults(e *BuildArtifactEntry) {
	if e.MarkerMode == "" {
		e.MarkerMode = MarkerModeAny
	}
	if e.ConfidenceCap == "" {
		e.ConfidenceCap = ConfidenceHigh
	}
	e.Markers = slices.Clone(e.Markers)
}

// validateBuildArtifact checks one entry; label prefixes every problem.
func validateBuildArtifact(pr *problemList, label string, e BuildArtifactEntry) {
	if !kebabID.MatchString(e.ID) {
		pr.add(label, "id must be a kebab-case identifier (a-z, 0-9, single dashes)")
	}
	if !kebabID.MatchString(e.Ecosystem) {
		pr.add(label, "ecosystem %q must be a kebab-case identifier", e.Ecosystem)
	}
	validateBuildArtifactDir(pr, label, e.Dir)
	validateBuildArtifactMarkers(pr, label, e)
	if !slices.Contains(knownConfidences, e.ConfidenceCap) {
		pr.add(label, "unknown confidence_cap %q (want one of %v)", e.ConfidenceCap, knownConfidences)
	}
	if e.RequireFileInside != "" && (!isPlainName(e.RequireFileInside) || strings.ContainsAny(e.RequireFileInside, "*?[")) {
		pr.add(label, "require_file_inside %q must be a literal file name", e.RequireFileInside)
	}
	if strings.TrimSpace(e.Description) == "" {
		pr.add(label, "description must not be empty")
	}
}

// validateBuildArtifactMarkers checks the marker globs and their mode.
func validateBuildArtifactMarkers(pr *problemList, label string, e BuildArtifactEntry) {
	for _, m := range e.Markers {
		if !isPlainName(m) {
			pr.add(label, "marker %q must be a file name or a glob on one, without separators", m)
		} else if _, err := path.Match(m, ""); err != nil {
			pr.add(label, "marker %q: %v", m, err)
		}
	}
	if !slices.Contains([]MarkerMode{MarkerModeAny, MarkerModeAll}, e.MarkerMode) {
		pr.add(label, "unknown marker_mode %q (want any or all)", e.MarkerMode)
	}
}

// validateBuildArtifactDir accepts a name or glob, or two such segments.
func validateBuildArtifactDir(pr *problemList, label, dir string) {
	if dir == "" {
		pr.add(label, "dir must not be empty")
		return
	}
	segs := strings.Split(dir, "/")
	if len(segs) > 2 {
		pr.add(label, "dir %q may have at most two segments", dir)
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, `\`) {
			pr.add(label, "dir %q has an empty, relative or backslash segment", dir)
			return
		}
		if _, err := path.Match(s, ""); err != nil {
			pr.add(label, "dir %q: %v", dir, err)
			return
		}
	}
}

// isPlainName reports whether s is a single non-empty path element.
func isPlainName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}
