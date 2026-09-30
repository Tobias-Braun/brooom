package buildartifacts

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// TestGoldenFinding pins the JSON shape of one finding so schema drift is a
// visible diff. Values that depend on the machine (temp path, ID derived
// from it, block-size dependent allocation) are normalized.
func TestGoldenFinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the golden file uses slash paths")
	}
	f := newFixture(t, false)
	f.write("package.json", "node_modules/a/index.js")
	f.settle(f.daysAgo(100))
	all := f.run()
	if len(all) != 1 {
		t.Fatalf("findings = %+v", all)
	}
	x := all[0]
	x.ID = "<id>"
	x.SizeBytes = 4096
	data, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(string(data), f.dir, "<root>") + "\n"
	path := filepath.Join("testdata", "finding.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("finding JSON changed (run with -update to accept):\n%s", got)
	}
}
