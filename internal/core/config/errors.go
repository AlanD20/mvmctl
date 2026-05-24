package config

import "mvmctl/internal/infra/errs"

// NewConfigError creates a config-domain DomainError with the given message.
// Error code is CodeConfigError, class is ClassValidation.
// The Op field is set to the provided operation name.
func NewConfigError(op, msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeConfigError,
		Message: msg,
		Op:      op,
		Class:   errs.ClassValidation,
	}
}
