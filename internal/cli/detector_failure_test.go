package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestDetectorFailureExitCodes reproduces #191: a detector that could not run
// and a detector that only left a note both exited 0, so a script could not
// tell a failed scan from a scan with warnings.
func TestDetectorFailureExitCodes(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  int
		wantFatal bool
	}{
		{"fatal detector error", errors.New("kaboom"), ExitDetectorFailed, true},
		{"wrapped fatal error", errors.Join(errors.New("a"), errors.New("b")), ExitDetectorFailed, true},
		{"note stays zero", detect.Note(errors.New("only a note")), ExitOK, false},
		{"no error", nil, ExitOK, false},
	}
	for _, format := range []string{"json", "plain", "ndjson", "table"} {
		for _, tc := range tests {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				needGit(t)
				isolate(t)
				repo := testutil.NewRepo(t)
				t.Chdir(repo.Dir)
				d := registerFake(t, detect.CategoryFiles, func(context.Context, *detect.Env, scope.Target, func(findings.Finding)) error {
					return tc.err
				})
				code, out, errOut := runScanCmd(t, "scan", "-d", d.name, "-f", format)
				if code != tc.wantCode {
					t.Fatalf("code %d, want %d (stdout %q stderr %q)", code, tc.wantCode, out, errOut)
				}
				if tc.wantCode != ExitOK && !strings.Contains(errOut, "detector failure") {
					t.Errorf("stderr %q does not explain the exit code", errOut)
				}
				if format == "json" && tc.err != nil {
					assertFatalFlag(t, out, tc.wantFatal)
				}
			})
		}
	}
}

// assertFatalFlag checks the machine-readable class of the single scan error
// in a json report.
func assertFatalFlag(t *testing.T, out string, want bool) {
	t.Helper()
	var r findings.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(r.Errors) != 1 {
		t.Fatalf("want one scan error, got %+v", r.Errors)
	}
	if r.Errors[0].Fatal != want {
		t.Errorf("Fatal = %v, want %v", r.Errors[0].Fatal, want)
	}
}

// TestSkippedTargetIsNote keeps skips (a broken per-repo config that still
// leaves other targets scanned) on the note side: no fatal flag, exit 0.
func TestSkippedTargetIsNote(t *testing.T) {
	r := &findings.Report{Errors: []findings.ScanError{{Path: "/x", Message: "target skipped: bad config"}}}
	if err := detectorFailure(r); err != nil {
		t.Fatalf("a note must not fail the scan: %v", err)
	}
	r.Errors = append(r.Errors, findings.ScanError{Detector: "d", Message: "boom", Fatal: true})
	var df detectorFailedError
	if err := detectorFailure(r); !errors.As(err, &df) {
		t.Fatalf("a fatal error must fail the scan, got %v", err)
	}
}
