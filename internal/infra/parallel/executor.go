package parallel

import (
	"context"
	"errors"
	"runtime"
	"sync"
)

// Parallel executes fn for each item concurrently with bounded concurrency.
// Workers = 0 means auto (runtime.NumCPU() * 2).
// All errors are collected and returned; execution continues on failure.
func Parallel[T any](ctx context.Context, workers int, items []T, fn func(context.Context, T) error) error {
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

// Map applies fn to each item concurrently, returning results in order.
// Workers = 0 means auto (runtime.NumCPU() * 2).
// Errors are collected and returned; execution continues on failure.
func Map[T, R any](ctx context.Context, workers int, items []T, fn func(context.Context, T) (R, error)) ([]R, error) {
	if workers <= 0 {
		workers = autoWorkers(len(items))
	}

	results := make([]R, len(items))
	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
	)
	sem := make(chan struct{}, workers)

	for i, item := range items {
		wg.Add(1)
		go func(idx int, it T) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r, err := fn(ctx, it)
			if err != nil {
				errsMu.Lock()
				errs = append(errs, err)
				errsMu.Unlock()
			} else {
				results[idx] = r
			}
		}(i, item)
	}

	wg.Wait()
	return results, errors.Join(errs...)
}

// autoWorkers calculates a default worker count:
//
//	min(n, (runtime.NumCPU() or 4) * 2)
func autoWorkers(n int) int {
	cpus := runtime.NumCPU()
	if cpus < 1 {
		cpus = 4
	}
	w := cpus * 2
	if n < w {
		w = n
	}
	if w < 1 {
		w = 1
	}
	return w
}
