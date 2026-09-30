package worktrees

import (
	"strings"
	"testing"
)

// TestInUseMessagePerOS: only the platforms whose check sees working
// directories may claim them (issue #213).
func TestInUseMessagePerOS(t *testing.T) {
	for goos, wantCwd := range map[string]bool{"linux": true, "darwin": true, "windows": false} {
		if got := strings.Contains(inUseMessage(goos), "working directory"); got != wantCwd {
			t.Errorf("%s: mentions working directory = %v, want %v", goos, got, wantCwd)
		}
	}
}
