package console

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// ── Console relay error helpers ────────────────────────────────────────
// Matches Python's services/console_relay/exceptions.py:
//   ConsoleRelayError, ConsoleRelayAlreadyRunningError,
//   ConsoleRelayProcessError, ConsoleRelayNotRunningError,
//   ConsoleRelayPermissionError, ConsoleRelayConnectionError

// ErrAlreadyRunning is returned when attempting to start a relay that is already running.
// Matches Python's ConsoleRelayAlreadyRunningError.
func ErrAlreadyRunning(id string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeConsoleRelayFailed,
		Op:      "console",
		Entity:  id,
		Message: fmt.Sprintf("Console relay already running for ID: %s", id),
		Class:   errs.ClassConflict,
	}
}

// ErrProcessFailed is returned when the relay process fails to start or terminates unexpectedly.
// Matches Python's ConsoleRelayProcessError.
func ErrProcessFailed(id string, err error) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeConsoleRelayFailed,
		Op:      "console",
		Entity:  id,
		Message: fmt.Sprintf("Failed to spawn console relay process: %s", err),
		Err:     err,
		Class:   errs.ClassInternal,
	}
}

// ErrNotRunning is returned when attempting to interact with a relay that is not running.
// Matches Python's ConsoleRelayNotRunningError.
func ErrNotRunning(id string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeConsoleRelayFailed,
		Op:      "console",
		Entity:  id,
		Message: "Console relay not running",
		Class:   errs.ClassValidation,
	}
}

// ErrPermission is returned when the relay lacks permission to access the console.
// Matches Python's ConsoleRelayPermissionError.
func ErrPermission(id string, err error) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeConsoleRelayFailed,
		Op:      "console",
		Entity:  id,
		Message: fmt.Sprintf("Permission denied for console relay: %s", err),
		Err:     err,
		Class:   errs.ClassNeedsInteraction,
	}
}

// ErrConnectionFailed is returned when the client fails to connect to the relay socket.
// Matches Python's ConsoleRelayConnectionError exactly:
//
//	"Failed to connect to console relay at {socket_path}: {e}"
func ErrConnectionFailed(socketPath string, err error) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeConsoleRelayFailed,
		Op:      "console",
		Message: fmt.Sprintf("Failed to connect to console relay at %s: %s", socketPath, err),
		Err:     err,
		Class:   errs.ClassInternal,
	}
}
