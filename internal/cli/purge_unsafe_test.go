package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// TestSkipOpenErrorDoesNotRepeatReportedPaths: with --gc the scan already
// printed the dubious repository, and purge used to print it a second time.
func TestSkipOpenErrorDoesNotRepeatReportedPaths(t *testing.T) {
	unsafe := &gitx.UnsafeRepoError{Dir: "/w/r", Stderr: "dubious ownership"}
	scanned := []findings.ScanError{
		{Path: "/w/r", Message: findings.SkipPrefix + unsafe.Error()},
		{Detector: "git-bloat", Path: "/w/other", Message: findings.SkipPrefix + "not a scan-level skip"},
	}
	reported := reportedSkips(scanned)
	var warn bytes.Buffer
	if err := skipOpenError(&warn, reported, "/w/r", unsafe); err != nil || warn.Len() != 0 {
		t.Errorf("already reported: err %v, warning %q; want silence", err, warn.String())
	}
	// A path the scan did not report is warned about once, however often asked.
	for i := 0; i < 2; i++ {
		_ = skipOpenError(&warn, reported, "/w/new", unsafe)
	}
	if n := strings.Count(warn.String(), "skipped: /w/new"); n != 1 {
		t.Errorf("warned %d times for /w/new, want 1: %q", n, warn.String())
	}
}

// TestSkipOpenError pins purge's handling of repositories it cannot open:
// dubious ownership is skipped with a visible line naming the fix, plain
// non-repositories silently, and real failures abort.
func TestSkipOpenError(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		err      error
		wantErr  error
		wantWarn string
	}{
		{"dubious ownership", &gitx.UnsafeRepoError{Dir: "/w/r", Stderr: "dubious ownership"}, nil, "safe.directory /w/r"},
		{"not a repo", gitx.ErrNotRepo, nil, ""},
		{"bare", gitx.ErrBareRepo, nil, ""},
		{"other", boom, boom, ""},
	}
	for _, tc := range tests {
		var warn bytes.Buffer
		if got := skipOpenError(&warn, map[string]bool{}, "/w/r", tc.err); !errors.Is(got, tc.wantErr) {
			t.Errorf("%s: err = %v, want %v", tc.name, got, tc.wantErr)
		}
		if tc.wantWarn == "" && warn.Len() != 0 || !strings.Contains(warn.String(), tc.wantWarn) {
			t.Errorf("%s: warning = %q, want %q", tc.name, warn.String(), tc.wantWarn)
		}
		if tc.wantWarn != "" && !strings.HasPrefix(warn.String(), "skipped: ") {
			t.Errorf("%s: warning = %q must start with \"skipped: \"", tc.name, warn.String())
		}
	}
}
