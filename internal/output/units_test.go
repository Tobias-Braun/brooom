package output

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// binaryUnit matches the IEC unit names ("KiB", "MiB", ...) that a local size
// formatter would print. Brooom shows every size with FormatSize (decimal SI),
// so a second formatter with binary units would make the same number read
// differently in the scan table, the plan and the sessions listing.
var binaryUnit = regexp.MustCompile(`%ciB|"KMGTPE?"`)

// TestNoOtherSizeFormatters fails when production code outside this package
// formats sizes with binary units, which is how the local humanBytes copies
// (sessions, completions, git-bloat evidence) disagreed with FormatSize.
func TestNoOtherSizeFormatters(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if binaryUnit.MatchString(line) {
				t.Errorf("%s:%d formats sizes with binary units: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFormatSizeIsDecimal(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 1024: "1.0 kB", 5_000_000: "5.0 MB"} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}
