package logs

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// ErrLogsNotFound creates a "log file not found" error.
func ErrLogsNotFound(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeValidationFailed,
		Op:      "logs",
		Message: msg,
		Class:   errs.ClassValidation,
	}
}

// ErrLogsReadFailed creates a "log read failed" error.
// Matches Python's LogsError(f"Error reading log file: {e}") raised in read_log_lines().
func ErrLogsReadFailed(err error) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeInternal,
		Op:      "logs",
		Message: fmt.Sprintf("error reading log file: %s", err),
		Err:     err,
		Class:   errs.ClassInternal,
	}
}

// ErrLogsFollowFailed creates a "log follow failed" error.
// Matches Python's LogsError(f"Error following log: {e}") raised in follow_log().
func ErrLogsFollowFailed(err error) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeInternal,
		Op:      "logs",
		Message: fmt.Sprintf("error following log: %s", err),
		Err:     err,
		Class:   errs.ClassInternal,
	}
}
