package vm

import (
	"context"
	"runtime"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/parallel"
)

// ── Parallel execution helpers matching Python's ParallelExecutor ──
// These are package-level functions (not methods) because Go doesn't allow
// generic methods on structs. The behavior matches Python exactly.

// executorItem is a generic pair of item and result/error.
type executorItem[T, R any] struct {
	Item   T
	Result R
}

// executeSeq runs functions sequentially (fail-fast on first error).
// Matches Python's ParallelExecutor._sequential().
func executeSeq[T, R any](items []T, fn func(T) R) []executorItem[T, R] {
	var results []executorItem[T, R]
	for _, item := range items {
		res := fn(item)
		results = append(results, executorItem[T, R]{item, res})
		// Check if result is an error — fail-fast (matches Python's "break")
		if err, ok := any(res).(error); ok && err != nil {
			break
		}
	}
	return results
}

// executePar runs functions in parallel using a worker pool.
// Matches Python's ParallelExecutor._execute_batch() + _parallel().
func executePar[T, R any](ctx context.Context, items []T, fn func(T) R, maxWorkers int, batchSize int) []executorItem[T, R] {
	var allResults []executorItem[T, R]

	for i := 0; i < len(items); i += batchSize {
		end := i + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		batchResults, _ := parallel.Map(ctx, maxWorkers, batch, func(_ context.Context, it T) (executorItem[T, R], error) {
			res := fn(it)
			return executorItem[T, R]{Item: it, Result: res}, nil
		})

		allResults = append(allResults, batchResults...)
	}

	return allResults
}

// execute runs func on each item, matching Python's ParallelExecutor.execute().
// Sequential mode: fail-fast on first error.
// Parallel mode: process all items, collect errors, continue on failure.
func execute[T, R any](
	ctx context.Context,
	items []T,
	fn func(T) R,
	parallel bool,
	maxWorkers int,
	batchSize int,
) []executorItem[T, R] {
	if parallel {
		n := len(items)
		if maxWorkers <= 0 {
			maxWorkers = min(n, runtime.NumCPU()*2)
		}
		if batchSize <= 0 {
			batchSize = n
		}
		if maxWorkers < 1 {
			maxWorkers = 1
		}
		if batchSize < 1 {
			batchSize = 1
		}
		return executePar(ctx, items, fn, maxWorkers, batchSize)
	}
	return executeSeq(items, fn)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildResult converts executorItem results to the shared errs.BulkResult.
func buildResult(items []executorItem[*model.VM, error]) *errs.BulkResult {
	result := &errs.BulkResult{
		Items: make([]errs.BulkResultItem, len(items)),
	}
	for i, r := range items {
		result.Items[i] = errs.BulkResultItem{Item: r.Item, Error: r.Result}
	}
	return result
}
