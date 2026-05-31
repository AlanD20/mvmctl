package infra

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
