package mergedbranch_test

import (
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// TestGlobalMinAgeDaysFloorsMergedBranches: thresholds.min_age_days above the
// built-in default hides merged branches whose tip is younger (the fixture
// clock is 60 days after the tip); the default keeps reporting them.
func TestGlobalMinAgeDaysFloorsMergedBranches(t *testing.T) {
	tests := []struct {
		name string
		days int
		want bool
	}{
		{"default keeps recent merges", config.DefaultMinAgeDays, true},
		{"floor below tip age", 30, true},
		{"floor above tip age", 365, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.feature("feat/done", "a.txt")
			f.merge("feat/done")
			f.publish()
			f.cfg.Thresholds.MinAgeDays = tt.days
			if got := byRef(f.detect(), "feat/done") != nil; got != tt.want {
				t.Errorf("reported = %v, want %v", got, tt.want)
			}
		})
	}
}
