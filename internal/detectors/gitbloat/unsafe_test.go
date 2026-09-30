package gitbloat

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// TestSkipNotRepo pins which errors mean "nothing to scan". A repository with
// dubious ownership must reach the scan errors: skipping it silently made the
// report look clean instead of partial.
func TestSkipNotRepo(t *testing.T) {
	unsafe := &gitx.UnsafeRepoError{Dir: "/w/repo", Stderr: "dubious ownership"}
	other := errors.New("boom")
	tests := []struct {
		name string
		in   error
		want error
	}{
		{"not a repo", gitx.ErrNotRepo, nil},
		{"bare", fmt.Errorf("wrapped: %w", gitx.ErrBareRepo), nil},
		{"dubious ownership", unsafe, unsafe},
		{"other", other, other},
	}
	for _, tc := range tests {
		if got := skipNotRepo(tc.in); !errors.Is(got, tc.want) {
			t.Errorf("%s: skipNotRepo = %v, want %v", tc.name, got, tc.want)
		}
	}
}
