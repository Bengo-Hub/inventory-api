package bulkjobs

import (
	"context"
	"sync"
)

// RunBounded runs fn once per item with at most `concurrency` running at any moment — the
// "queueing" every bulk operation should use so a large batch never opens more concurrent DB
// transactions/cascade emissions/GL postings than the system can comfortably absorb at once.
// Blocks until every item has been attempted (success or failure); returns one error per item,
// in the same order as items (nil = succeeded). ctx cancellation stops launching new work but
// already-started items still run to completion.
func RunBounded[T any](ctx context.Context, items []T, concurrency int, fn func(context.Context, T) error) []error {
	if concurrency < 1 {
		concurrency = 1
	}
	errs := make([]error, len(items))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, item := range items {
		select {
		case <-ctx.Done():
			errs[i] = ctx.Err()
			continue
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, item T) {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = fn(ctx, item)
		}(i, item)
	}
	wg.Wait()
	return errs
}
