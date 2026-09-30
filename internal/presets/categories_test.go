package presets

import (
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/catalog"
)

// TestNonSafeLogCategoriesExistInCatalog keeps the hardcoded ids in sync with
// the catalog: a renamed category would otherwise silently stop being
// switched off by the safe preset.
func TestNonSafeLogCategoriesExistInCatalog(t *testing.T) {
	var known []string
	for _, c := range catalog.Categories() {
		known = append(known, string(c))
	}
	for _, id := range nonSafeLogCategories {
		if !slices.Contains(known, id) {
			t.Errorf("nonSafeLogCategories has %q, which is not a catalog category (%v)", id, known)
		}
	}
}
