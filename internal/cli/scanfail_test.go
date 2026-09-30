package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// brokenRepoFixture is a repository whose .brooom.json is invalid (below the
// threshold floor), so the whole repository is skipped with a scan error and
// nothing is scanned.
func brokenRepoFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	f := newCleanupFixture(t, nil)
	testutil.WriteFile(t, f.repo.Dir, ".brooom.json", `{"thresholds":{"min_age_days":0}}`)
	return f
}

// TestScanFailureIsVisible reproduces #113: a repository that could not be
// scanned looked like a clean one in plain output and exited 0.
func TestScanFailureIsVisible(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"scan plain", []string{"scan", "-f", "plain"}},
		{"root plain", []string{"-f", "plain"}},
		{"artifacts plain", []string{"artifacts", "-f", "plain"}},
		{"scan table", []string{"scan"}},
		{"scan summary", []string{"scan", "-f", "summary"}},
		{"scan json", []string{"scan", "-f", "json"}},
		{"artifacts dry run", []string{"artifacts"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			brokenRepoFixture(t)
			code, out, errOut := brooom(t, "", tc.args...)
			if code != ExitScanFailed {
				t.Errorf("exit code %d, want %d (stderr %q)", code, ExitScanFailed, errOut)
			}
			if !strings.Contains(out+errOut, "min_age_days") {
				t.Errorf("the scan error is not shown anywhere:\nstdout %q\nstderr %q", out, errOut)
			}
			if strings.Contains(out, "Nothing to sweep.") {
				t.Errorf("claims nothing to sweep although nothing was scanned:\n%s", out)
			}
		})
	}
}

// TestScanErrorChannels checks that a formatter without an in-band channel
// gets the error on stderr, and one with it does not repeat it.
func TestScanErrorChannels(t *testing.T) {
	brokenRepoFixture(t)
	_, out, errOut := brooom(t, "", "scan", "-f", "plain")
	if out != "" {
		t.Errorf("plain stdout must stay empty, got %q", out)
	}
	if !strings.Contains(errOut, "scan error:") {
		t.Errorf("stderr lacks the scan error: %q", errOut)
	}
	_, out, errOut = brooom(t, "", "scan", "-f", "json")
	var r findings.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil || !strings.Contains(out, "min_age_days") {
		t.Errorf("json carries the error in-band: %v %+v", err, r.Errors)
	}
	if strings.Contains(errOut, "scan error:") {
		t.Errorf("json repeats the error on stderr: %q", errOut)
	}
}

// TestCleanScanKeepsExitZero pins that a scan that covered its targets exits 0.
func TestCleanScanKeepsExitZero(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, _, errOut := brooom(t, "", "scan", "-f", "plain")
	if code != ExitOK {
		t.Errorf("clean scan exit %d, stderr %q", code, errOut)
	}
}
