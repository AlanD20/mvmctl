package enricher

import (
	"context"
	"sort"
	"strings"

	"mvmctl/internal/infra/parallel"
)

// sortByDotCount sorts relation paths by dot count (parents before children).
// Matches Python's sorted(include, key=lambda p: p.count(".")).
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
// Uses parallel.Map for goroutine-based parallelism.
func BatchResolveParallel[T any](
	ctx context.Context,
	ids []string,
	fn BatchResolveFunc[T],
	maxWorkers int,
) map[string]T {
	// Resolve each ID concurrently; collect results for callers that want partial success.
	type idResult struct {
		ID  string
		Val T
		Err error
	}

	results, _ := parallel.Map(ctx, maxWorkers, ids, func(ctx context.Context, id string) (idResult, error) {
		val, err := fn(id)
		return idResult{ID: id, Val: val, Err: err}, nil
	})

	resultMap := make(map[string]T, len(results))
	for _, r := range results {
		if r.Err == nil {
			resultMap[r.ID] = r.Val
		}
	}
	return resultMap
}
