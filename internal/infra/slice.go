package infra

import (
	"sort"
	"strings"

	"mvmctl/pkg/errs"
)

// Dedup removes duplicate elements from a slice while preserving order. Uses T's
// comparable constraint for O(n) dedup with a map.
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

// SortedKeys returns the keys of m sorted alphabetically.
func SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// JoinStringsPtrs joins error messages from a BatchResult.
func JoinStringsPtrs(result *errs.BatchResult) string {
	if result == nil {
		return ""
	}
	msgs := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		if item.Message != "" {
			msgs = append(msgs, item.Message)
		}
	}
	return strings.Join(msgs, "; ")
}

// IsTrue returns true for typical truthy string values.
func IsTrue(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val == "1" || val == "true" || val == "yes" || val == "on"
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	default:
		return false
	}
}
