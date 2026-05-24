package volume

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// NewVolumeError creates a generic volume operation error matching Python's VolumeError.
// Python VolumeError is raised when a volume operation fails with a specific message.
// Python's VolumeError(MVMError) does not set a specific error code (code=None),
// so CodeVolumeError is used as the domain-specific fallback, matching the pattern
// used by other domains (e.g., CodeImageError for ImageError).
func NewVolumeError(msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeVolumeError,
		Op:      "volume",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// NewVolumeErrorf creates a formatted volume error.
func NewVolumeErrorf(format string, args ...any) *errs.DomainError {
	return NewVolumeError(fmt.Sprintf(format, args...))
}

// ErrVolumeNotFound creates a "volume not found" error matching Python's
// VolumeNotFoundError(f"Volume not found: {volume_id!r}").
// The message includes the entity in single quotes matching Python repr style.
func ErrVolumeNotFound(entity string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeVolumeNotFound,
		Op:      "volume",
		Message: fmt.Sprintf("Volume not found: '%s'", entity),
		Class:   errs.ClassValidation,
	}
}

// ErrVolumeNotFoundByName creates a "volume not found by name" error matching Python's
// VolumeNotFoundError(f"Volume not found by name: {name!r}").
func ErrVolumeNotFoundByName(name string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeVolumeNotFound,
		Op:      "volume",
		Message: fmt.Sprintf("Volume not found by name: '%s'", name),
		Class:   errs.ClassValidation,
	}
}

// ErrVolumeAmbiguous creates a "volume ID is ambiguous" error matching Python's
// VolumeNotFoundError(f"Volume ID is ambiguous: {volume_id!r}").
func ErrVolumeAmbiguous(id string) *errs.DomainError {
	return &errs.DomainError{
		Code:    errs.CodeVolumeNotFound,
		Op:      "volume",
		Message: fmt.Sprintf("Volume ID is ambiguous: '%s'", id),
		Class:   errs.ClassValidation,
	}
}

// ErrVolumeAlreadyExists creates a "volume already exists" error.
func ErrVolumeAlreadyExists(entity string) *errs.DomainError {
	return &errs.DomainError{
		Code:   errs.CodeVolumeAlreadyExists,
		Op:     "volume",
		Entity: entity,
		Class:  errs.ClassConflict,
	}
}
