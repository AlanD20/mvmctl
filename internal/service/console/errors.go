package console

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// ErrConnectionFailed is returned when the client fails to connect to the relay socket.
func ErrConnectionFailed(socketPath string, err error) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeConsoleRelayFailed,
		Op:      "console",
		Message: fmt.Sprintf("Failed to connect to console relay at %s: %s", socketPath, err),
		Err:     err,
		Class:   errs.ClassInternal,
	}
}
