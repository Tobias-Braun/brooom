package buildartifacts

import (
	"runtime"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// TestActionCommandQuotesPath: the suggested trash command must keep a path
// with spaces or shell metacharacters as one word.
func TestActionCommandQuotesPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the expected commands use POSIX quoting; Windows quoting is covered in findings")
	}
	tests := map[string]string{
		"/p/node_modules": "trash -- /p/node_modules",
		"/-dash/dist":     "trash -- /-dash/dist",
		"/my proj/dist":   "trash -- '/my proj/dist'",
		"/p;rm/dist":      "trash -- '/p;rm/dist'",
	}
	s := &scan{env: &detect.Env{}}
	for path, want := range tests {
		f := facts{path: path, c: candidate{m: match{rule: &rule{}}}}
		got := s.action(f, nil)
		if got.Type != findings.ActionTrash || got.Command != want {
			t.Errorf("action for %q = %+v, want command %q", path, got, want)
		}
	}
}
