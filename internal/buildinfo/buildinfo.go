// Package buildinfo exposes the version information stamped into the binary
// at release time via -ldflags (see .goreleaser.yaml):
//
//	-X github.com/Tobias-Braun/brooom/internal/buildinfo.Version=1.2.3
//	-X github.com/Tobias-Braun/brooom/internal/buildinfo.Commit=<sha>
//	-X github.com/Tobias-Braun/brooom/internal/buildinfo.Date=<rfc3339>
//
// Builds without ldflags (go install, go run) fall back to the module
// version and VCS information embedded by the Go toolchain.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Set via -ldflags at release time.
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

// Info is the resolved build information.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get returns the build information, filling gaps from the embedded module
// and VCS data.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		fillFromBuildInfo(&info, bi)
	}
	info.Version = orDefault(info.Version, "dev")
	info.Commit = orDefault(info.Commit, "unknown")
	info.Date = orDefault(info.Date, "unknown")
	return info
}

// fillFromBuildInfo fills fields not set via ldflags from the module version
// and VCS stamps the Go toolchain embeds.
func fillFromBuildInfo(info *Info, bi *debug.BuildInfo) {
	if info.Version == "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}
	settings := map[string]string{}
	for _, s := range bi.Settings {
		settings[s.Key] = s.Value
	}
	info.Commit = orDefault(info.Commit, settings["vcs.revision"])
	info.Date = orDefault(info.Date, settings["vcs.time"])
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
