package config

import (
	"fmt"

	"mvmctl/internal/infra"
)

// OverridableSettings maps category -> key -> type name.
// Computed from infra.OverridableDefaults at init.
// Call InitSettings() before accessing.
var OverridableSettings map[string]map[string]string

// InitSettings initializes OverridableSettings from infra.OverridableDefaults.
// Replaces the former init() — must be called explicitly from app startup.

func InitSettings() {
	OverridableSettings = make(map[string]map[string]string, len(infra.OverridableDefaults))
	for cat, keys := range infra.OverridableDefaults {
		typeMap := make(map[string]string, len(keys))
		for key, val := range keys {
			typeMap[key] = GoTypeName(val)
		}
		OverridableSettings[cat] = typeMap
	}
}

// GoTypeName returns the Go type name for a value.
// These names are stored in the OverridableSettings database for type-based coercion.
func GoTypeName(v any) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case bool:
		return "bool"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case float32, float64:
		return "float"
	case string:
		return "string"
	case map[string]any:
		return "map"
	case []any:
		return "slice"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// GetExpectedType returns the expected type name for a setting.
func GetExpectedType(category, key string) string {
	cat, ok := OverridableSettings[category]
	if !ok {
		return ""
	}
	return cat[key]
}
