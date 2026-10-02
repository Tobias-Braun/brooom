package cli

import (
	"os"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

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
