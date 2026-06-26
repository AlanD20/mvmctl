package enricher

import (
	"context"
	"sort"
	"strings"

	"mvmctl/internal/infra/pool"
)

// sortByDotCount sorts relation paths by dot count (parents before children).
func sortByDotCount(paths []string) []string {
	sorted := make([]string, len(paths))
	copy(sorted, paths)
	sort.SliceStable(sorted, func(i, j int) bool {
		return strings.Count(sorted[i], ".") < strings.Count(sorted[j], ".")
	})
	return sorted
}

// BatchResolveFunc is a function that resolves a single ID.
type BatchResolveFunc[T any] func(id string) (T, error)

// BatchResolveParallel resolves multiple IDs in parallel using goroutines.
// Returns a map from ID to resolved value, collecting all results or errors.
// Uses pool.Gather for goroutine-based parallelism.
func BatchResolveParallel[T any](
	ctx context.Context,
	ids []string,
	fn BatchResolveFunc[T],
	maxWorkers int,
) map[string]T {
	results := pool.Gather(ctx, maxWorkers, ids, func(ctx context.Context, id string) (T, error) {
		return fn(id)
	})

	resultMap := make(map[string]T, len(results))
	for i, r := range results {
		if r.Err == nil {
			resultMap[ids[i]] = r.Value
		}
	}
	return resultMap
}
