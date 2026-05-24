package nocloudnet

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// ErrNoCloudServerError creates a generic nocloud server error matching Python's NoCloudServerError.
func ErrNoCloudServerError(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeCloudInitNetModeFailed,
		Message: msg,
		Op:      "nocloudnet",
		Class:   errs.ClassInternal,
	}
}

// ErrNoCloudServerAlreadyRunning creates an already-running error matching Python's NoCloudServerAlreadyRunningError.
func ErrNoCloudServerAlreadyRunning(id string) error {
	return &errs.DomainError{
		Code:    errs.CodeCloudInitNetModeFailed,
		Message: fmt.Sprintf("NoCloud-net server already running: %s", id),
		Op:      "nocloudnet",
		Entity:  id,
		Class:   errs.ClassConflict,
	}
}
