package presets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// TestSiteMirrorsPresets keeps the landing page's preset tabs
// (site/src/lib/presets.ts) in line with the definitions here: the page
// promises exactly what a preset sweeps, so every detector, confidence floor
// and Includes line must appear in that preset's entry, in the same order.
func TestSiteMirrorsPresets(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "site", "src", "lib", "presets.ts"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	entries := strings.Split(src, "    id: '")[1:]
	if len(entries) != len(all()) {
		t.Fatalf("site lists %d presets, Go defines %d", len(entries), len(all()))
	}
	for i, p := range all() {
		entry := entries[i]
		if !strings.HasPrefix(entry, p.Name+"'") {
			t.Errorf("site preset %d is not %q", i, p.Name)
			continue
		}
		want := []string{
			fmt.Sprintf("summary: '%s'", p.Summary),
			fmt.Sprintf("isDefault: %t", p.Name == config.DefaultPreset),
			fmt.Sprintf("detectors: ['%s']", strings.Join(p.Detectors, "', '")),
			fmt.Sprintf("minConfidence: '%s'", p.MinConfidence),
		}
		for d, c := range p.Floors {
			want = append(want, fmt.Sprintf("'%s': '%s'", d, c))
		}
		if len(p.Floors) == 0 {
			want = append(want, "floors: {}")
		}
		want = append(want, "includes: [\n      '"+strings.Join(p.Includes, "',\n      '")+"',\n    ]")
		for _, w := range want {
			if !strings.Contains(entry, w) {
				t.Errorf("site preset %s lacks %s", p.Name, w)
			}
		}
	}
}
