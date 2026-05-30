package host

import "mvmctl/internal/infra/errs"

// hostError creates a DomainError matching Python's HostError(message).
func hostError(code errs.Code, msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    code,
		Message: msg,
		Class:   errs.ClassInternal,
	}
}
