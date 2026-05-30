package infra

import (
	"fmt"
)

// RequireString extracts a required string field from a YAML map.
// Returns an error if the key is missing or the value is not a string.
// Error messages match Python's yaml.require_str() format.
// Python uses data.get(key) which returns None for missing keys, and
// raises: "field '<key>' must be a string (got 'NoneType')".
// For wrong types, Python produces: "field '<key>' must be a string (got '<typename>')".
func RequireString(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("field '%s' must be a string (got 'NoneType')", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("field '%s' must be a string (got '%T')", key, v)
	}
	return s, nil
}

// OptionalString returns a pointer to the string value of key,
// or nil if absent or not a string.
// Mirrors Python's mvmctl.utils.yaml.optional_str().
func OptionalString(data map[string]any, key string) *string {
	v, ok := data[key]
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

// OptionalInt returns a pointer to the integer value of key,
// or nil if absent or not an integer (or bool).
// Mirrors Python's mvmctl.utils.yaml.optional_int().
//
// Python's optional_int accepts int AND bool (because isinstance(True, int)
// is True in Python — bool is a subclass of int). YAML true/false decode as
// bool in Go's yaml.v3, so we must accept bool as valid int (true→1, false→0).
//
// float64 values (even round ones like 42.0) are rejected, matching Python's
// behavior where isinstance(42.0, int) returns False.
func OptionalInt(data map[string]any, key string) *int {
	v, ok := data[key]
	if !ok {
		return nil
	}
	switch n := v.(type) {
	case int:
		return &n
	case bool:
		if n {
			one := 1
			return &one
		}
		zero := 0
		return &zero
	default:
		return nil
	}
}

// RequireStrList returns the value of key as a list of strings.
// An absent key is treated as an empty list. Returns an error if the value
// is present but not a list of strings.
// Mirrors Python's mvmctl.utils.yaml.require_str_list().
func RequireStrList(data map[string]any, key string) ([]string, error) {
	v, ok := data[key]
	if !ok {
		return []string{}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("field '%s' must be a list of strings", key)
	}
	result := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("field '%s' must be a list of strings", key)
		}
		result = append(result, s)
	}
	return result, nil
}

// SetValEntry represents a parsed option/value pair from YAML.
// Matches Python's _OptionValuePair named tuple.
type SetValEntry struct {
	Option string
	Value  string
}

// ParseSetValList parses option/value pairs from YAML data map under the given key.
// Each entry in the list should be either:
//   - a map with "option" and "value" keys (like Python dict entries), or
//   - a two-element list/tuple where item[0]=option, item[1]=value
//
// Matches Python's parse_set_val_list() function.
func ParseSetValList(data map[string]any, key string) ([]SetValEntry, error) {
	v, ok := data[key]
	if !ok {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("field '%s' must be a list", key)
	}
	var result []SetValEntry
	for _, item := range list {
		switch entry := item.(type) {
		case map[string]any:
			option, ok := entry["option"].(string)
			if !ok {
				return nil, fmt.Errorf("field '%s' entries must have a string 'option' field", key)
			}
			value, ok := entry["value"].(string)
			if !ok {
				return nil, fmt.Errorf("field '%s' entries must have a string 'value' field", key)
			}
			result = append(result, SetValEntry{Option: option, Value: value})
		case []any:
			if len(entry) != 2 {
				return nil, fmt.Errorf("field '%s' list entries must have exactly 2 elements (option, value)", key)
			}
			option, ok := entry[0].(string)
			if !ok {
				return nil, fmt.Errorf("field '%s' list entry option must be a string", key)
			}
			value, ok := entry[1].(string)
			if !ok {
				return nil, fmt.Errorf("field '%s' list entry value must be a string", key)
			}
			result = append(result, SetValEntry{Option: option, Value: value})
		default:
			return nil, fmt.Errorf("field '%s' entries must be {option, value} mappings or two-element lists", key)
		}
	}
	return result, nil
}
