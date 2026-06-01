// Package ptr provides generic pointer and nil-safe deref helpers.
//
// This consolidates scattered strPtr, intPtr, boolPtr, safeDeref functions
// that were duplicated across pkg/api/, internal/cli/, internal/core/.
// Python has no direct equivalent — these are porting conveniences.
package ptr

// StrNonEmpty returns a pointer to s if s is non-empty, or nil if s is empty.
// Matches Python's pattern of returning None for empty strings.
func StrNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// SafeDeref returns the string value pointed to by s, or "" if s is nil.
func SafeDeref(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}

// SafeDerefInt returns the int value pointed to by i, or 0 if i is nil.
func SafeDerefInt(i *int) int {
	if i != nil {
		return *i
	}
	return 0
}
