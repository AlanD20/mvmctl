package infra

// BoolToInt converts a bool to an int (1 for true, 0 for false).
// Used for SQLite storage where bools are stored as integers.
func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
