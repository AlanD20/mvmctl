package ssh

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// ErrCPSourceFailed creates a CP source-failed error with exit code templating.
// Matches Python's CPError(..., code="cp.source_failed").
func ErrCPSourceFailed(exitCode int) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeCPSourceFailed,
		Op:      "cp",
		Message: fmt.Sprintf("source tar process failed (exit %d)", exitCode),
		Class:   errs.ClassInternal,
	}
}


