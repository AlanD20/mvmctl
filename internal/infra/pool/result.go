package pool

// Result pairs a value with an optional error.
// Each item in a pool operation produces exactly one Result.
type Result[T any] struct {
	Value T
	Err   error
}
