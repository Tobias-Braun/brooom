package scope

import "testing"

// TestExcludedCaseFolding pins the case handling of the matcher: folded on
// case-insensitive filesystems (issue #136: a user-excluded path must not
// reach the plan because of its spelling) and exact elsewhere.
func TestExcludedCaseFolding(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		rel      string
		fold     bool
		want     bool
	}{
		{"base name folded", []string{"Scratch"}, "a/scratch", true, true},
		{"base name exact when not folded", []string{"Scratch"}, "a/scratch", false, false},
		{"anchored folded", []string{"Src/*/Gen"}, "src/x/GEN", true, true},
		{"double star folded", []string{"**/Scratch"}, "A/B/SCRATCH", true, true},
		{"glob folded", []string{"Build-*"}, "x/BUILD-arm", true, true},
		{"same spelling not folded", []string{"Scratch"}, "Scratch", false, true},
		{"different name stays unmatched", []string{"Scratch"}, "scratchy", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := excluded(tt.patterns, tt.rel, tt.fold); got != tt.want {
				t.Fatalf("excluded(%q, %q, fold=%v) = %v, want %v", tt.patterns, tt.rel, tt.fold, got, tt.want)
			}
		})
	}
}

// TestExcludedFollowsPlatform checks that the exported function uses the
// platform's folding decision.
func TestExcludedFollowsPlatform(t *testing.T) {
	if got, want := Excluded([]string{"Scratch"}, "scratch"), foldNames(); got != want {
		t.Fatalf("Excluded folds = %v, want foldNames() = %v", got, want)
	}
}
