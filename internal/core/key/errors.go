package key

import "mvmctl/internal/infra/errs"

// keyError wraps a DomainError with Python-compatible Error() format.
// keyError.Error() returns just the message, matching Python's str(exception)
// where the code/op prefix is not included.
//
// Usage:
//
//	return nil, &keyError{err: errs.MVMKeyError("message")}
//
// Callers can extract the underlying DomainError via errors.As:
//
//	var de *errs.DomainError
//	if errors.As(err, &de) { ... }
type keyError struct {
	err *errs.DomainError
}

func (e *keyError) Error() string {
	return e.err.Message
}

func (e *keyError) Unwrap() error {
	return e.err
}
