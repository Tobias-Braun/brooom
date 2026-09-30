package detect

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestMapOrderedKeepsInputOrder(t *testing.T) {
	items := make([]int, 200)
	for i := range items {
		items[i] = i
	}
	got := MapOrdered(context.Background(), items, 8, func(_ context.Context, n int) int {
		// Later items finish first, so any ordering by completion would show.
		time.Sleep(time.Duration(len(items)-n) * 10 * time.Microsecond)
		return n * 2
	})
	for i, v := range got {
		if v != i*2 {
			t.Fatalf("result %d = %d, want %d", i, v, i*2)
		}
	}
}

func TestMapOrderedBoundsConcurrency(t *testing.T) {
	var running, peak atomic.Int64
	MapOrdered(context.Background(), make([]int, 50), 3, func(context.Context, int) int {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		running.Add(-1)
		return 0
	})
	if p := peak.Load(); p > 3 || p < 2 {
		t.Errorf("peak concurrency %d, want between 2 and 3", p)
	}
}

func TestMapOrderedStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int64
	MapOrdered(ctx, make([]int, 1000), 2, func(context.Context, int) int {
		if ran.Add(1) == 5 {
			cancel()
		}
		return 0
	})
	if n := ran.Load(); n >= 1000 {
		t.Errorf("ran %d items after cancellation", n)
	}
}

func TestMapOrderedEmpty(t *testing.T) {
	if got := MapOrdered(context.Background(), []int(nil), 4, func(context.Context, int) int { return 1 }); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
