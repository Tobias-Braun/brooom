package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
)

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
		if got := skipOpenError(&warn, "/w/r", tc.err); !errors.Is(got, tc.wantErr) {
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
