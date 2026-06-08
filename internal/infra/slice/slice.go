package slice

import (
	"strings"

	"mvmctl/pkg/errs"
)

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
func IsTrue(v interface{}) bool {
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
