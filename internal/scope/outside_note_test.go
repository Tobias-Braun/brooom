package scope_test

import (
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/scope"
)

// TestOutsideNote pins when the hint is given: only for a path that resolves
// outside the allowed locations. An empty path is unknown and a path inside
// gets no note.
func TestOutsideNote(t *testing.T) {
	g, work, main := metaGuard(t)
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"empty path", "", ""},
		{"inside", work, ""},
		{"below inside", filepath.Join(work, "missing"), ""},
		{"outside", filepath.Dir(work), scope.OutsideWorktreeHint},
		{"repository metadata is outside", main, scope.OutsideWorktreeHint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.OutsideNote(tc.path); got != tc.want {
				t.Errorf("OutsideNote(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
