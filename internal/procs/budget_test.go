package procs

import "testing"

func TestBudgetScalesWithPathCountAndIsCapped(t *testing.T) {
	tests := []struct {
		n    int
		want int64
	}{
		{0, int64(DefaultTimeout)},
		{1, int64(DefaultTimeout)},
		{2, int64(DefaultTimeout + PerPathBudget)},
		{101, int64(DefaultTimeout + 100*PerPathBudget)},
		{1_000_000, int64(MaxBudget)},
	}
	for _, tt := range tests {
		if got := int64(Budget(tt.n)); got != tt.want {
			t.Errorf("Budget(%d) = %v, want %v", tt.n, got, tt.want)
		}
	}
}
