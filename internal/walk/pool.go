package walk

import (
	"context"
	"sync"
)

// workQueue is an unbounded FIFO shared by a fixed set of workers. Because
// pushing never blocks, a worker that discovers a thousand subdirectories
// cannot deadlock the pool the way a bounded channel could.
type workQueue[T any] struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []T
	pending int // queued plus in-flight items; zero means the work is done
}

func (q *workQueue[T]) push(item T) {
	q.mu.Lock()
	q.items = append(q.items, item)
	q.pending++
	q.mu.Unlock()
	q.cond.Signal()
}

// finish marks one popped item as processed and wakes everyone when the
// queue has drained so idle workers can exit.
func (q *workQueue[T]) finish() {
	q.mu.Lock()
	q.pending--
	drained := q.pending == 0
	q.mu.Unlock()
	if drained {
		q.cond.Broadcast()
	}
}

// pop blocks until an item is available. It returns false when all work is
// done or ctx is cancelled.
func (q *workQueue[T]) pop(ctx context.Context) (item T, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && q.pending > 0 && ctx.Err() == nil {
		q.cond.Wait()
	}
	if ctx.Err() != nil || len(q.items) == 0 {
		return item, false
	}
	item = q.items[0]
	var zero T
	q.items[0] = zero // release references held by the slice prefix
	q.items = q.items[1:]
	return item, true
}

// wakeAll broadcasts under the lock so a waiter that checked the context
// just before cancellation cannot miss the wakeup.
func (q *workQueue[T]) wakeAll() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cond.Broadcast()
}

// runPool processes seed and everything fn submits with a bounded number of
// workers, shared by Walk and the cold path of DirSize. It returns ctx.Err()
// if the context was cancelled and only returns after all workers exited, so
// no goroutine outlives the call.
func runPool[T any](ctx context.Context, workers int, seed T, fn func(item T, submit func(T))) error {
	q := &workQueue[T]{}
	q.cond = sync.NewCond(&q.mu)
	stop := context.AfterFunc(ctx, q.wakeAll)
	defer stop()

	q.push(seed)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				item, ok := q.pop(ctx)
				if !ok {
					return
				}
				fn(item, q.push)
				q.finish()
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}
