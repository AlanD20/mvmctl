package pool

import (
	"context"
	"errors"
	"runtime"
	"sync"
)

// Do executes fn for each item concurrently with bounded concurrency.
// Workers = 0 means auto (runtime.NumCPU() * 2).
// All errors are collected and returned; execution continues on failure.
// Returns nil if no errors occurred.
// If ctx is canceled, remaining items are skipped; already-submitted work finishes.
func Do[T any](ctx context.Context, workers int, items []T, fn func(context.Context, T) error) error {
	if workers <= 0 {
		workers = autoWorkers(len(items))
	}

	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
	)
	sem := make(chan struct{}, workers)

	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(it T) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := fn(ctx, it); err != nil {
				errsMu.Lock()
				errs = append(errs, err)
				errsMu.Unlock()
			}
		}(item)
	}

	wg.Wait()
	return errors.Join(errs...)
}

// Gather executes fn for each item concurrently, collecting results in order.
// Workers = 0 means auto (runtime.NumCPU() * 2).
// Execution continues on error; all results are populated (errors in Result.Err).
// If ctx is canceled, remaining items are skipped; already-submitted work finishes.
func Gather[T, R any](ctx context.Context, workers int, items []T, fn func(context.Context, T) (R, error)) []Result[R] {
	if workers <= 0 {
		workers = autoWorkers(len(items))
	}

	results := make([]Result[R], len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)

	for i, item := range items {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(idx int, it T) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			v, err := fn(ctx, it)
			results[idx] = Result[R]{Value: v, Err: err}
		}(i, item)
	}

	wg.Wait()
	return results
}

// Seq executes fn for each item sequentially, stopping on first error.
// Results are ordered; items after the first error are zero-valued.
func Seq[T, R any](ctx context.Context, items []T, fn func(context.Context, T) (R, error)) []Result[R] {
	results := make([]Result[R], len(items))
	for i, item := range items {
		v, err := fn(ctx, item)
		results[i] = Result[R]{Value: v, Err: err}
		if err != nil {
			break
		}
	}
	return results
}

// autoWorkers calculates a default worker count:
//
//	min(n, (runtime.NumCPU() or 4) * 2)
func autoWorkers(n int) int {
	cpus := runtime.NumCPU()
	if cpus < 1 {
		cpus = 4
	}
	w := min(cpus*2, n)
	return max(w, 1)
}
