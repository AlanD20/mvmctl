package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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
			typeMap[key] = goTypeName(val)
		}
		OverridableSettings[cat] = typeMap
	}
}

// goTypeName returns the Go type name for a value.
// These names are stored in the OverridableSettings database for type-based coercion.
// Note: Python used "NoneType"/"str"/"dict"/"list" but Go uses "nil"/"string"/"map"/"slice".
// This is a deliberate behavioral change per the Porting Spec (Verdict 28).
// Database values created by the Python codebase will need migration.
func goTypeName(v any) string {
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

// typeError returns a formatted type mismatch error using Go type names.
// Matches Python's TypeError("Expected {expected_type.__name__}, got {type(value).__name__}").
func typeError(expected string, got any) error {
	return fmt.Errorf("Expected %s, got %s", expected, goTypeName(got))
}

// GetExpectedType returns the expected type name for a setting.
func GetExpectedType(category, key string) string {
	cat, ok := OverridableSettings[category]
	if !ok {
		return ""
	}
	return cat[key]
}

// GetDefault returns the hardcoded default value for a setting from infra.
func GetDefault(category, key string) (any, error) {
	return infra.GetDefault(category, key)
}

// Coerce coerces a value to the expected type.
// Matches Python CommonUtils.coerce() in src/mvmctl/utils/common.py.
//
// Accepts Go-style type names ("string", "map", "nil", "slice").
// Error messages always use Go-style type names per Verdict 28.
func Coerce(value any, expectedType string) (any, error) {
	// Use Go-style type name for internal handling
	goType := expectedType

	switch goType {
	case "bool":
		switch v := value.(type) {
		case bool:
			return v, nil
		case string:
			lower := strings.ToLower(v) // no trim — matches Python behavior
			return lower == "true" || lower == "1" || lower == "yes" || lower == "on", nil
		default:
			return nil, typeError(expectedType, value)
		}

	case "int":
		switch v := value.(type) {
		case int:
			return v, nil
		case bool:
			// Python: bool is a subclass of int — isinstance(True, int) → True
			return v, nil
		case string:
			n, err := strconv.Atoi(v) // no trim — matches Python int() behavior
			if err != nil {
				return nil, fmt.Errorf("invalid literal for int() with base 10: '%s'", v)
			}
			return n, nil
		default:
			return nil, typeError(expectedType, value)
		}

	case "float":
		switch v := value.(type) {
		case float64:
			return v, nil
		case string:
			f, err := strconv.ParseFloat(v, 64) // no trim — matches Python float() behavior
			if err != nil {
				return nil, fmt.Errorf("could not convert string to float: '%s'", v)
			}
			return f, nil
		default:
			return nil, typeError(expectedType, value)
		}

	case "string":
		if s, ok := value.(string); ok {
			return s, nil
		}
		return nil, typeError(expectedType, value)

	case "map":
		switch v := value.(type) {
		case map[string]any:
			return v, nil
		case string:
			var result map[string]any
			if err := json.Unmarshal([]byte(v), &result); err != nil {
				return nil, fmt.Errorf("invalid JSON dict value: %s", v)
			}
			return result, nil
		default:
			return nil, typeError(expectedType, value)
		}

	case "nil":
		if value == nil {
			return nil, nil
		}
		return nil, typeError(expectedType, value)

	case "slice":
		switch v := value.(type) {
		case []any:
			return v, nil
		default:
			return nil, typeError(expectedType, value)
		}

	default:
		// Unknown expected type — return value as-is (matches Python behavior)
		return value, nil
	}
}

// MarshalValue marshals a Go value to its JSON string representation.
func MarshalValue(v any) (string, error) {
	if v == nil {
		return "null", nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
