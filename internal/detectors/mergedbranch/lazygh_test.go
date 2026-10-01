package mergedbranch_test

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestOpenPRAskedOnlyForMergedBranches: gh is a network call, so it is only
// made once a merged branch needs the answer, and then once per repository.
func TestOpenPRAskedOnlyForMergedBranches(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	f.setGH(func(context.Context, string, []string, ...string) ([]byte, error) {
		calls.Add(1)
		return []byte(`[]`), nil
	})
	f.feature("feat/open", "o.txt")
	f.publish()
	if got := f.detect(); len(got) != 0 || calls.Load() != 0 {
		t.Fatalf("no merged branch: findings %v, gh calls %d", got, calls.Load())
	}
	f.feature("feat/a", "a.txt")
	f.feature("feat/b", "b.txt")
	f.merge("feat/a")
	f.merge("feat/b")
	f.publish()
	if got := f.detect(); len(got) != 2 || calls.Load() != 1 {
		t.Fatalf("two merged branches: %d findings, gh calls %d", len(got), calls.Load())
	}
}
