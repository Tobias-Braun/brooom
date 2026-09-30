package gitx_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestPathWithinOS(t *testing.T) {
	tests := []struct {
		goos, dir, p string
		want         bool
	}{
		{"linux", "/a/b", "/a/b", true},
		{"linux", "/a/b", "/a/b/c/d", true},
		{"linux", "/a/b", "/a/bc", false},
		{"linux", "/a/b", "/a", false},
		{"linux", "/a/b", "/a/B/c", false},
		{"darwin", "/a/b", "/A/B/c", true},
		{"darwin", "/a/b", "/a/bc", false},
		{"linux", "/", "/x", true},
	}
	for _, tt := range tests {
		if got := gitx.PathWithinOS(tt.goos, tt.dir, tt.p); got != tt.want {
			t.Errorf("%s PathWithin(%q, %q) = %v, want %v", tt.goos, tt.dir, tt.p, got, tt.want)
		}
	}
}

func TestCwdWithin(t *testing.T) {
	root := testutil.ResolvedTempDir(t)
	sub := filepath.Join(root, "wt", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if !gitx.CwdWithin(filepath.Join(root, "wt")) || !gitx.CwdWithin(sub) {
		t.Error("cwd is inside wt but was not detected")
	}
	if gitx.CwdWithin(filepath.Join(root, "other")) || gitx.CwdWithin(filepath.Join(root, "wt", "dee")) {
		t.Error("cwd is not inside a sibling directory")
	}
}
