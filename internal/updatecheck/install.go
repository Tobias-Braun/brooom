package updatecheck

import (
	"path/filepath"
	"strings"
)

// Install methods reported by DetectInstall.
const (
	MethodBrew   = "brew"
	MethodScoop  = "scoop"
	MethodGo     = "go"
	MethodManual = "manual"
)

// PackagesPublished says whether the Homebrew tap and Scoop bucket exist yet.
// While false, brew and scoop commands are printed as suggestions only.
const PackagesPublished = false

// ReleasesURL is the human-facing releases page used for manual installs.
const ReleasesURL = "https://github.com/Tobias-Braun/brooom/releases"

// GoInstallCmd is the upgrade command for `go install` users.
const GoInstallCmd = "go install github.com/Tobias-Braun/brooom/cmd/brooom@latest"

// InstallEnv is the part of the environment install detection depends on,
// passed in explicitly so the detection is table testable on every OS.
type InstallEnv struct {
	// GOBIN is $GOBIN, empty when unset.
	GOBIN string
	// GOPATH holds the entries of $GOPATH (already split); when empty the
	// Go default Home/go applies.
	GOPATH []string
	// Home is the user's home directory, used for the default GOPATH.
	Home string
}

// Install describes how the running binary was installed and how to upgrade.
type Install struct {
	Method string
	// Upgrade is the command or instruction that upgrades the binary.
	Upgrade string
	// Suggestion is true when Upgrade is a package manager command whose
	// package is not published yet.
	Suggestion bool
}

// DetectInstall infers the install method from the resolved executable
// path. Detection is heuristic and purely lexical: it never touches the
// filesystem so synthetic Windows and Unix paths test the same everywhere.
func DetectInstall(exePath string, env InstallEnv) Install {
	p := normalizePath(exePath)
	switch {
	// Casks live in <prefix>/Caskroom, which on Intel Macs (/usr/local) has
	// no "homebrew" path element, unlike the Apple Silicon prefix.
	case strings.Contains(p, "/cellar/") || strings.Contains(p, "/caskroom/") || strings.Contains(p, "/homebrew/"):
		return Install{Method: MethodBrew, Upgrade: "brew upgrade brooom", Suggestion: !PackagesPublished}
	case strings.Contains(p, "scoop/apps"):
		return Install{Method: MethodScoop, Upgrade: "scoop update brooom", Suggestion: !PackagesPublished}
	case underGoBin(p, env):
		return Install{Method: MethodGo, Upgrade: GoInstallCmd}
	}
	return Install{Method: MethodManual, Upgrade: "download the latest release from " + ReleasesURL}
}

// normalizePath lowercases and converts separators so matching works for
// Windows paths on any OS. Filesystems on macOS and Windows are usually
// case-insensitive, and a false negative only degrades to "manual".
func normalizePath(p string) string {
	return strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
}

func underGoBin(p string, env InstallEnv) bool {
	var dirs []string
	if env.GOBIN != "" {
		dirs = append(dirs, env.GOBIN)
	}
	// Without GOPATH the Go default <home>/go applies. A bare "/go/bin/"
	// substring is not used: it also matches the Go toolchain directory.
	gopath := env.GOPATH
	if len(gopath) == 0 && env.Home != "" {
		gopath = []string{filepath.Join(env.Home, "go")}
	}
	for _, g := range gopath {
		if g != "" {
			dirs = append(dirs, g+"/bin")
		}
	}
	for _, d := range dirs {
		d = strings.TrimRight(normalizePath(d), "/")
		if d != "" && strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}
