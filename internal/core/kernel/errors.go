package kernel

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// NewKernelError creates a kernel error matching Python's KernelError.
// Uses CodeKernelBuildFailed as the default. For specific error codes
// (e.g. CodeKernelConfigFailed), use NewKernelErrorWithCode.
func NewKernelError(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeKernelBuildFailed,
		Op:      "kernel",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// NewKernelErrorf creates a kernel error with a formatted message.
func NewKernelErrorf(format string, args ...any) *errs.DomainError {
	return NewKernelError(fmt.Sprintf(format, args...))
}

// NewKernelErrorWithCode creates a kernel error with a specific error code.
// Use when the error context requires a more specific code than the default
// CodeKernelError, e.g. CodeKernelBuildFailed for build pipeline failures
// or CodeKernelConfigFailed for kernel configuration failures.
func NewKernelErrorWithCode(code errs.Code, msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    code,
		Op:      "kernel",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// NewKernelErrorfWithCode creates a kernel error with a specific error code
// and a formatted message.
func NewKernelErrorfWithCode(code errs.Code, format string, args ...any) *errs.DomainError {
	return NewKernelErrorWithCode(code, fmt.Sprintf(format, args...))
}

// KernelNotFoundError creates a "kernel not found" error matching Python's KernelNotFoundError.
func KernelNotFoundError(entity string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeKernelNotFound,
		Op:      "kernel.resolve",
		Entity:  entity,
		Class:   errs.ClassValidation,
	}
}

// KernelConfigError creates a kernel config error matching Python's KernelConfigError.
func KernelConfigError(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeKernelConfigFailed,
		Op:      "kernel",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// ChecksumMismatchError creates a checksum mismatch error for kernel-domain operations.
// Python's ChecksumMismatchError inherits from ImageError but is also used in the
// kernel domain. Maps to CodeKernelBuildFailed since there is no kernel-specific
// checksum code.
func ChecksumMismatchError(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeKernelBuildFailed,
		Op:      "kernel",
		Message: msg,
		Class:   errs.ClassRetryable,
	}
}
