package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// writeRaw stores content in a fresh temp file and returns its path.
func writeRaw(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "findings.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// junkDir creates a directory with a file in it and returns both paths.
func junkDir(t *testing.T, parent, name string) (dir, file string) {
	t.Helper()
	file = testutil.WriteFile(t, filepath.Join(parent, name), "data.txt", "x")
	return filepath.Dir(file), file
}

// oldJunk creates an OS junk file in dir that is old enough for the tidy
// preset and returns its path.
func oldJunk(t *testing.T, dir string) string {
	t.Helper()
	p := testutil.WriteFile(t, dir, ".DS_Store", "x")
	if err := os.Chtimes(p, testutil.BaseTime, testutil.BaseTime); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
