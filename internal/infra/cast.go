package infra

import (
	"strconv"
	"strings"
)

// ToString converts any value to a string. Returns defaultVal on failure.
func ToString(v any, defaultVal string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return defaultVal
}

// ToInt converts any value to an int. Returns defaultVal on failure.
func ToInt(v any, defaultVal int) int {
	if v == nil {
		return defaultVal
	}
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

// ToBool converts any value to a bool. Returns defaultVal on failure.
func ToBool(v any, defaultVal bool) bool {
	if v == nil {
		return defaultVal
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	case int, int64, float64:
		return val != 0
	}
	return defaultVal
}

// BoolToInt converts a bool to an int (1 for true, 0 for false).
// Used for SQLite storage where bools are stored as integers.
func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DerefOrZero returns the value pointed to by p, or the zero value of T if p is nil.
func DerefOrZero[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// DerefOrNil returns the value pointed to by p as any, or nil if p is nil.
// Useful for SQL INSERT args where nil maps to SQL NULL.
func DerefOrNil[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// ShlexQuote returns a shell-safe single-quoted version of s.
// Matches Python's shlex.quote(): safe characters pass through, everything
// else is wrapped in single quotes with embedded quotes escaped as '"'"'.
func ShlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !('a' <= r && r <= 'z') && !('A' <= r && r <= 'Z') &&
			!('0' <= r && r <= '9') &&
			r != '@' && r != '%' && r != '_' && r != '+' &&
			r != '=' && r != ':' && r != ',' && r != '.' &&
			r != '/' && r != '-' {
			return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
		}
	}
	return s
}
