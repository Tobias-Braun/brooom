package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/updatecheck"
)

// updateStatus is how the running version relates to the latest release.
type updateStatus int

const (
	statusUpToDate updateStatus = iota
	statusUpdate
	statusAhead
	statusUnknown // dev build or unparsable version: no comparison possible
)

// updateReport is the machine-readable result of `update-check`.
type updateReport struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	URL             string `json:"url"`
	InstallMethod   string `json:"install_method"`
	Upgrade         string `json:"upgrade"`
}

func newUpdateCheckCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "update-check",
		Short: "Check GitHub for a newer release (opt-in, contacts api.github.com)",
		Example: `  brooom update-check
  brooom update-check --format json`,
		Long: `Ask GitHub once whether a newer Brooom release exists. Running this command
is your consent to that single request: it is unauthenticated and sends no
data about you. Brooom never checks on its own unless you set
"update_check": true in the config file.

Brooom does not update itself; it only prints how to upgrade for the way it
was installed. Set BROOOM_UPDATE_URL to point the check at a mirror.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUpdateCheck(cmd)
		},
	}
}

func (a *app) runUpdateCheck(cmd *cobra.Command) error {
	switch a.flags.format {
	case "", "table", "plain", "json":
	default:
		return usageError{fmt.Errorf("update-check supports --format table, plain or json, not %q", a.flags.format)}
	}
	base := updatecheck.BaseURL()
	if !a.flags.quiet {
		fmt.Fprintf(a.io.Err, "Contacting %s (no data about you is sent)\n", updatecheck.LatestURL(base))
	}
	rel, err := updatecheck.Latest(cmd.Context(), updatecheck.NewClient(base), base)
	if err != nil {
		return fmt.Errorf("update check failed: %w", err)
	}
	current := a.currentVersion()
	status := compareVersions(current, rel.TagName)
	install := updatecheck.DetectInstall(a.executablePath(), installEnv())
	// Tags carry a "v", the running version usually does not; JSON consumers
	// compare the two, so both are reported bare.
	report := updateReport{
		Current:         strings.TrimPrefix(current, "v"),
		Latest:          strings.TrimPrefix(rel.TagName, "v"),
		UpdateAvailable: status == statusUpdate,
		URL:             rel.HTMLURL,
		InstallMethod:   install.Method,
		Upgrade:         install.Upgrade,
	}
	if a.flags.format == "json" {
		enc := json.NewEncoder(a.io.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return a.printUpdateText(report, status, install)
}

// compareVersions classifies current against latest. Dev builds and versions
// that do not parse are "unknown" rather than an error: the user cannot act
// on a parse failure of their own binary's version.
func compareVersions(current, latest string) updateStatus {
	if updatecheck.IsDevVersion(current) {
		return statusUnknown
	}
	cur, err := updatecheck.ParseVersion(current)
	if err != nil {
		return statusUnknown
	}
	lat, err := updatecheck.ParseVersion(latest)
	if err != nil {
		return statusUnknown
	}
	switch cur.Compare(lat) {
	case -1:
		return statusUpdate
	case 1:
		return statusAhead
	}
	return statusUpToDate
}

func (a *app) printUpdateText(r updateReport, status updateStatus, install updatecheck.Install) error {
	cur, lat := strings.TrimPrefix(r.Current, "v"), strings.TrimPrefix(r.Latest, "v")
	var line string
	var details []string
	switch status {
	case statusUpToDate:
		line = fmt.Sprintf("brooom %s is up to date", cur)
	case statusAhead:
		line = fmt.Sprintf("brooom %s is newer than the latest release (%s)", cur, lat)
	case statusUpdate:
		line = fmt.Sprintf("brooom %s is available (you have %s): %s", lat, cur, r.URL)
		details = append(details, "Upgrade: "+upgradeHint(install))
	default:
		line = fmt.Sprintf("development build (%s): no comparison possible; the latest release is %s: %s", orUnknown(cur), lat, r.URL)
		details = append(details, "Install it: "+upgradeHint(install))
	}
	fmt.Fprintln(a.io.Out, line)
	if a.flags.quiet {
		return nil
	}
	for _, d := range details {
		fmt.Fprintln(a.io.Out, d)
	}
	return nil
}

// upgradeHint renders the install instruction and flags package manager
// commands as suggestions while their packages are not published.
func upgradeHint(install updatecheck.Install) string {
	s := install.Upgrade
	if install.Suggestion {
		s += " (suggestion only: the " + install.Method + " package is not published yet)"
	}
	return s
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// executablePath returns the symlink-resolved path of the running binary, or
// "" when it cannot be determined (detection then falls back to manual).
func (a *app) executablePath() string {
	get := a.update.executable
	if get == nil {
		get = os.Executable
	}
	p, err := get()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// installEnv reads the Go-related environment install detection needs.
func installEnv() updatecheck.InstallEnv {
	home, _ := os.UserHomeDir()
	return updatecheck.InstallEnv{
		GOBIN:  os.Getenv("GOBIN"),
		GOPATH: filepath.SplitList(os.Getenv("GOPATH")),
		Home:   home,
	}
}
