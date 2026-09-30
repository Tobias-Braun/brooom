package stalebranch

import (
	"fmt"
	"slices"
	"testing"
)

// TestManyBranchesEmitInBranchOrder: branches are assessed by a worker pool,
// but findings must come out in branch name order on every run.
func TestManyBranchesEmitInBranchOrder(t *testing.T) {
	f := newFixture(t, true)
	var want []string
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("old/b%02d", i)
		f.pushed(name, f.daysAgo(120))
		want = append(want, name)
	}
	slices.Sort(want)
	for run := 0; run < 3; run++ {
		var got []string
		for _, fd := range f.mustDetect() {
			got = append(got, fd.Ref)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("run %d: refs %v, want %v", run, got, want)
		}
	}
}
