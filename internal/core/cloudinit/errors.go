package cloudinit

import "mvmctl/internal/infra/errs"

// ErrCloudInitFailed creates a generic cloud-init error matching Python's CloudInitError.
// Python: CloudInitError(MVMError) — base class for all cloud-init errs.
func ErrCloudInitFailed(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeCloudInitProvisionFailed,
		Op:      "cloudinit",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// ErrCloudInitProvisionFailed creates a cloud-init provision error matching Python's CloudInitProvisionError.
// Python: CloudInitProvisionError(CloudInitError) — subclass for invalid custom user data.
func ErrCloudInitProvisionFailed(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeCloudInitProvisionFailed,
		Op:      "cloudinit",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// ErrCloudInitNetModeFailed creates a cloud-init net mode error matching Python's CloudInitNetModeError.
func ErrCloudInitNetModeFailed(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeCloudInitNetModeFailed,
		Op:      "cloudinit",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}

// ErrCloudInitISOModeFailed creates a cloud-init ISO mode error matching Python's CloudInitIsoModeError.
func ErrCloudInitISOModeFailed(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeCloudInitISOModeFailed,
		Op:      "cloudinit",
		Message: msg,
		Class:   errs.ClassInternal,
	}
}
