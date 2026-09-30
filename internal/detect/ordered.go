package detect

import (
	"context"
	"sync"
	"sync/atomic"
)

// BranchWorkers bounds the goroutines that classify the branches of one
// repository. The engine already runs (target, detector) pairs in parallel, so
// this only has to hide the latency of the git processes a single repository
// with many branches costs, without flooding the machine with them.
const BranchWorkers = 4

// MapOrdered applies fn to every item with at most workers goroutines and
// returns the results in the order of items, so the output of a detector does
// not depend on scheduling. Once ctx is cancelled no further item is started;
// the results of the items never run stay zero, and callers check ctx.Err()
// afterwards. fn must be safe for concurrent use.
func MapOrdered[T, R any](ctx context.Context, items []T, workers int, fn func(context.Context, T) R) []R {
	out := make([]R, len(items))
	workers = max(1, min(workers, len(items)))
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(items) {
					return
				}
				out[i] = fn(ctx, items[i])
			}
		}()
	}
	wg.Wait()
	return out
}
