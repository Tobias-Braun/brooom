package scope

import (
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultMaxDepth is the discovery depth used when DiscoverOptions.MaxDepth
// is 0. It equals config.Default().Scan.MaxDepth (asserted by a test, since
// scope does not import config).
const DefaultMaxDepth = 6

// makefileMarker is the one marker that never makes a directory a project on
// its own: plain Makefiles show up in far too many non-project folders.
const makefileMarker = "Makefile"

// projectMarkerNames are the exact file names that mark a non-git project
// folder. To add a marker, append its name here (or its extension to
// projectMarkerSuffixes) and extend the table in markers_test.go; every
// consumer (Discover, the catalog, project-aware detectors) picks it up
// through IsProjectMarker and HasProjectMarker.
var projectMarkerNames = []string{
	"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "setup.py",
	"requirements.txt", "Pipfile", "Gemfile", "composer.json", "pom.xml",
	"build.gradle", "build.gradle.kts", "settings.gradle",
	"settings.gradle.kts", "mix.exs", "pubspec.yaml", "Package.swift",
	"deno.json", "deno.jsonc", "CMakeLists.txt", makefileMarker,
}

// projectMarkerSuffixes are file extensions that mark a project folder
// (Visual Studio project and solution files have arbitrary base names).
var projectMarkerSuffixes = []string{".csproj", ".sln"}

// HugeDirNames are directory names that discovery never descends into: they
// hold dependency or tool trees that are enormous and never contain
// workspace targets. Matching is exact on unix and case-insensitive on
// Windows and macOS. Library and AppData are not listed because they are
// only skipped directly below the user's home (see Discover). Treat the
// slice as read-only.
var HugeDirNames = []string{
	"node_modules", ".venv", "venv", "target", ".gradle", "vendor", ".git",
	".cache", ".npm", ".cargo", ".rustup", ".m2", ".Trash", "$RECYCLE.BIN",
	"System Volume Information",
}

// homeOnlySkipNames are skipped only when they are a direct child of the
// user's home directory, so a project folder that merely happens to be named
// Library elsewhere is still scanned.
var homeOnlySkipNames = []string{"Library", "AppData"}

// foldNames reports whether file names compare case-insensitively on the
// current OS (the default filesystems of Windows and macOS).
func foldNames() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }

// sameName compares two names with the OS's case rules.
func sameName(a, b string) bool {
	return a == b || (foldNames() && strings.EqualFold(a, b))
}

// IsProjectMarker reports whether name is a project marker file name. Note
// that Makefile is in the table but only counts together with another
// marker; use HasProjectMarker to decide about a directory.
func IsProjectMarker(name string) bool {
	for _, m := range projectMarkerNames {
		if sameName(m, name) {
			return true
		}
	}
	ext := filepath.Ext(name)
	for _, s := range projectMarkerSuffixes {
		if ext != "" && sameName(s, ext) {
			return true
		}
	}
	return false
}

// HasProjectMarker reports whether a directory whose entry names are given
// is a project folder: it holds at least one marker other than Makefile.
func HasProjectMarker(names []string) bool {
	for _, n := range names {
		if !sameName(n, makefileMarker) && IsProjectMarker(n) {
			return true
		}
	}
	return false
}

// ProjectMarkers returns a copy of the exact marker names, for consumers
// (docs, catalog) that need to list them. Suffix patterns are not included.
func ProjectMarkers() []string {
	return append([]string(nil), projectMarkerNames...)
}
