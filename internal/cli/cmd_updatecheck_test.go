package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/updatecheck"
)

// releaseFixture is a fake GitHub API answering the latest-release endpoint.
type releaseFixture struct {
	srv  *httptest.Server
	hits atomic.Int32
}

func newReleaseFixture(t *testing.T, status int, tag string, delay time.Duration) *releaseFixture {
	t.Helper()
	f := &releaseFixture{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://example.test/releases/` + tag + `"}`))
		} else {
			_, _ = w.Write([]byte("SECRET-BODY"))
		}
	}))
	t.Cleanup(f.srv.Close)
	t.Setenv(updatecheck.BaseURLEnv, f.srv.URL)
	return f
}

// newTestApp returns an app with a throwaway Brooom home, no config file and
// injected collaborators; nothing touches the real home or the network.
func newTestApp(t *testing.T, version string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv(config.HomeEnv, t.TempDir())
	t.Setenv(NoUpdateCheckEnv, "")
	var out, errOut bytes.Buffer
	a := &app{io: IO{In: strings.NewReader(""), Out: &out, Err: &errOut}}
	a.update.version = func() string { return version }
	a.update.executable = func() (string, error) { return "/usr/local/bin/brooom", nil }
	return a, &out, &errOut
}

func enableBackground(a *app) {
	a.update.stdoutTTY = func() bool { return true }
	a.update.loadConfig = func(string) (*config.Config, error) {
		return &config.Config{UpdateCheck: true}, nil
	}
}

func TestUpdateCheckResults(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    string
		extra   string
	}{
		{"newer available", "1.2.0", "v1.4.0", "brooom 1.4.0 is available (you have 1.2.0): https://example.test/releases/v1.4.0", "Upgrade: download the latest release"},
		{"up to date", "v1.4.0", "v1.4.0", "brooom 1.4.0 is up to date", ""},
		{"current is newer", "1.5.0", "v1.4.0", "brooom 1.5.0 is newer than the latest release (1.4.0)", ""},
		{"prerelease current below release", "1.4.0-rc.1", "v1.4.0", "brooom 1.4.0 is available", ""},
		{"dev build", "dev", "v1.4.0", "development build (dev): no comparison possible; the latest release is 1.4.0", "Install it:"},
		{"empty version", "", "v1.4.0", "development build (unknown)", "Install it:"},
		{"pseudo version", "v0.0.0-20240101120000-abcdef123456", "v1.4.0", "no comparison possible", "Install it:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newReleaseFixture(t, 200, tt.latest, 0)
			a, out, errOut := newTestApp(t, tt.current)
			if code := execute(a, []string{"update-check"}); code != ExitOK {
				t.Fatalf("code = %d, stderr %q", code, errOut)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Errorf("stdout %q does not contain %q", out, tt.want)
			}
			if tt.extra != "" && !strings.Contains(out.String(), tt.extra) {
				t.Errorf("stdout %q does not contain %q", out, tt.extra)
			}
			if !strings.HasPrefix(errOut.String(), "Contacting ") || !strings.Contains(errOut.String(), "(no data about you is sent)") {
				t.Errorf("stderr = %q, want contact notice", errOut)
			}
			if strings.Contains(out.String(), "Contacting") {
				t.Error("contact notice must go to stderr")
			}
		})
	}
}

func TestUpdateCheckContactLineNamesRequest(t *testing.T) {
	t.Setenv(updatecheck.BaseURLEnv, "")
	srv := newReleaseFixture(t, 200, "v1.0.0", 0)
	a, _, errOut := newTestApp(t, "1.0.0")
	if execute(a, []string{"update-check"}) != ExitOK {
		t.Fatal(errOut.String())
	}
	if !strings.Contains(errOut.String(), srv.srv.URL+updatecheck.LatestPath) {
		t.Errorf("stderr %q does not name the contacted URL", errOut)
	}
}

func TestUpdateCheckQuiet(t *testing.T) {
	newReleaseFixture(t, 200, "v2.0.0", 0)
	a, out, errOut := newTestApp(t, "1.0.0")
	if code := execute(a, []string{"update-check", "--quiet"}); code != ExitOK {
		t.Fatal(errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("quiet stderr = %q", errOut)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 1 {
		t.Errorf("quiet stdout must be one line, got %q", out)
	}
}

func TestUpdateCheckJSON(t *testing.T) {
	newReleaseFixture(t, 200, "v1.4.0", 0)
	a, out, errOut := newTestApp(t, "1.2.0")
	a.update.executable = func() (string, error) { return "/opt/homebrew/Cellar/brooom/1.2.0/bin/brooom", nil }
	if code := execute(a, []string{"update-check", "--format", "json"}); code != ExitOK {
		t.Fatal(errOut.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	want := map[string]any{
		"current": "1.2.0", "latest": "v1.4.0", "update_available": true,
		"url": "https://example.test/releases/v1.4.0", "install_method": "brew", "upgrade": "brew upgrade brooom",
	}
	if len(got) != len(want) {
		t.Errorf("keys = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestUpdateCheckJSONDevBuild(t *testing.T) {
	newReleaseFixture(t, 200, "v1.4.0", 0)
	a, out, _ := newTestApp(t, "dev")
	if execute(a, []string{"update-check", "-f", "json", "-q"}) != ExitOK {
		t.Fatal("dev json must exit 0")
	}
	var got updateReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.UpdateAvailable || got.Latest != "v1.4.0" {
		t.Errorf("report = %+v, err %v", got, err)
	}
}

func TestUpdateCheckPackageManagerSuggestion(t *testing.T) {
	newReleaseFixture(t, 200, "v2.0.0", 0)
	a, out, _ := newTestApp(t, "1.0.0")
	a.update.executable = func() (string, error) { return `C:\Users\u\scoop\apps\brooom\current\brooom.exe`, nil }
	execute(a, []string{"update-check"})
	if !strings.Contains(out.String(), "scoop update brooom") || !strings.Contains(out.String(), "suggestion only") {
		t.Errorf("stdout = %q", out)
	}
}

func TestUpdateCheckFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"server error", 500, "HTTP 500"},
		{"rate limited", 403, "rate limited"},
		{"too many requests", 429, "rate limited"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newReleaseFixture(t, tt.status, "", 0)
			a, out, errOut := newTestApp(t, "1.0.0")
			if code := execute(a, []string{"update-check"}); code != ExitError {
				t.Fatalf("code = %d, want %d", code, ExitError)
			}
			if !strings.Contains(errOut.String(), tt.want) || strings.Contains(errOut.String(), "SECRET") {
				t.Errorf("stderr = %q", errOut)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty on failure", out)
			}
		})
	}
}

func TestUpdateCheckServerDown(t *testing.T) {
	f := newReleaseFixture(t, 200, "v1.0.0", 0)
	f.srv.Close()
	a, _, errOut := newTestApp(t, "1.0.0")
	if code := execute(a, []string{"update-check"}); code != ExitError || !strings.Contains(errOut.String(), "update check failed") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestUpdateCheckTimeout(t *testing.T) {
	old := updatecheck.Timeout
	updatecheck.Timeout = 100 * time.Millisecond
	t.Cleanup(func() { updatecheck.Timeout = old })
	newReleaseFixture(t, 200, "v1.0.0", 5*time.Second)
	a, _, errOut := newTestApp(t, "1.0.0")
	start := time.Now()
	if code := execute(a, []string{"update-check"}); code != ExitError {
		t.Fatalf("code = %d, stderr %q", code, errOut)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("timeout was not honoured")
	}
}

func TestUpdateCheckBadFormat(t *testing.T) {
	a, _, _ := newTestApp(t, "1.0.0")
	if code := execute(a, []string{"update-check", "--format", "tree"}); code != ExitUsage {
		t.Errorf("code = %d, want %d", code, ExitUsage)
	}
}

func TestUpdateCheckWritesNothingToDisk(t *testing.T) {
	newReleaseFixture(t, 200, "v2.0.0", 0)
	a, _, _ := newTestApp(t, "1.0.0")
	execute(a, []string{"update-check"})
	home, _ := config.Home()
	if _, err := os.Stat(filepath.Join(home, "cache", "update.json")); err == nil {
		t.Error("the explicit command must not write the background cache")
	}
}

func TestCompareVersionsUnparsableIsUnknown(t *testing.T) {
	if compareVersions("garbage", "v1.0.0") != statusUnknown || compareVersions("1.0.0", "garbage") != statusUnknown {
		t.Error("unparsable versions must give no comparison")
	}
}

func TestExecutablePathFailure(t *testing.T) {
	a := &app{}
	a.update.executable = func() (string, error) { return "", errors.New("boom") }
	if a.executablePath() != "" {
		t.Error("failed lookup must yield empty path")
	}
}
