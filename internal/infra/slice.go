package infra

// Dedup removes duplicate elements from a slice while preserving order.
// Uses T's comparable constraint for O(n) dedup with a map.
func Dedup[T comparable](items []T) []T {
	seen := make(map[T]struct{}, len(items))
	result := make([]T, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; !ok {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}
