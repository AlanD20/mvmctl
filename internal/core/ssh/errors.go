package ssh

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// ErrSSHFailed creates an SSH error with the given message.
func ErrSSHFailed(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeSSHError,
		Op:      "ssh",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// ErrCPFailed creates a generic CP error with the given message.
func ErrCPFailed(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeCPError,
		Op:      "cp",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// ErrCPSourceNotFound creates a CP source-not-found error.
// Matches Python's CPSourceNotFoundError(code="cp.source_not_found").
func ErrCPSourceNotFound(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeCPSourceNotFound,
		Op:      "cp",
		Message: msg,
		Class:   errs.ClassValidation,
	}
}

// ErrCPDestinationExists creates a CP destination-exists error.
// Matches Python's CPDestinationExistsError(code="cp.destination_exists").
func ErrCPDestinationExists(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeCPDestinationExists,
		Op:      "cp",
		Message: msg,
		Class:   errs.ClassValidation,
	}
}

// ErrCPDestinationNotDir creates a CP destination-not-directory error.
// Matches Python's CPDestinationNotDirectoryError.
func ErrCPDestinationNotDir(path string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeCPDestinationNotDir,
		Op:      "cp",
		Message: fmt.Sprintf("Destination is not a directory: %s", path),
		Class:   errs.ClassValidation,
	}
}

// ErrCPSourceFailed creates a CP source-failed error.
// Matches Python's CPError(..., code="cp.source_failed").
func ErrCPSourceFailed(exitCode int) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeCPSourceFailed,
		Op:      "cp",
		Message: fmt.Sprintf("Source tar process failed (exit %d)", exitCode),
		Class:   errs.ClassInternal,
	}
}

// ErrCPCopyFailed creates a CP copy-failed error.
// Matches Python's CPError(..., code="cp.copy_failed").
func ErrCPCopyFailed(exitCode int, stderr string) *errs.DomainError {
	msg := fmt.Sprintf("Copy failed (exit %d)", exitCode)
	if stderr != "" {
		msg += ": " + stderr
	}
	return &errs.DomainError{
		Code:    errs.CodeCPCopyFailed,
		Op:      "cp",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// ErrCPDestinationFailed creates a CP destination-failed error.
// Matches Python's CPError(..., code="cp.destination_failed").
func ErrCPDestinationFailed(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeCPDestinationFailed,
		Op:      "cp",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}
