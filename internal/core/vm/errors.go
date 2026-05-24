package vm

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// Pre-built error templates matching Python exceptions.
// Python: VMNotFoundError → Go: NotFound(CodeVMNotFound, ...)
// Python: VMStateError → Go: ValidationFailed(CodeVMStateInvalid, ...)

// ErrVMNotFound creates a "VM not found" error matching Python's VMNotFoundError.
// Python: raise VMNotFoundError(f"VM not found: {vm_id}")
func ErrVMNotFound(entity string) error {
	return errs.NotFound(errs.CodeVMNotFound, fmt.Sprintf("VM not found: %s", entity))
}

// ErrVMAlreadyExists creates a "VM already exists" error.
// Python: raise VMAlreadyExistsError(f"VM already exists: {vm_id}")
func ErrVMAlreadyExists(entity string) error {
	return errs.AlreadyExists(errs.CodeVMAlreadyExists, fmt.Sprintf("VM already exists: %s", entity))
}

// ErrVMStateInvalid creates a VM state error matching Python's VMStateError(message).
func ErrVMStateInvalid(msg string) error {
	return errs.ValidationFailed(errs.CodeVMStateInvalid, msg)
}

// ErrVMResolveFailed creates a VM resolution error matching Python's VMNotFoundError
// with "ID xxx matches multiple VMs: ..." or "VM not found: ...".
func ErrVMResolveFailed(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeVMResolveFailed,
		Message: msg,
		Op:      "vm.resolve",
		Class:   errs.ClassValidation,
	}
}

// ErrVMCreateFailed creates a VM create failure error matching Python's VMCreateError.
// Python: VMCreateError — "VM creation failed - resources may have been partially created."
// The caller should perform best-effort cleanup of any partially-created resources.
func ErrVMCreateFailed(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeVMCreateFailed,
		Message: msg,
		Op:      "vm.create",
		Class:   errs.ClassInternal,
	}
}

// ErrVMBuilderFailed creates a VM builder failure error matching Python's VMBuilderError.
// Python: VMBuilderError — "VM builder failed - resources may have been partially created."
// The caller should perform best-effort cleanup of any partially-created resources.
func ErrVMBuilderFailed(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeVMBuilderFailed,
		Message: msg,
		Op:      "vm.create",
		Class:   errs.ClassInternal,
	}
}
