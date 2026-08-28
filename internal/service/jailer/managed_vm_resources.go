package jailer

import "mvmctl/pkg/errs"

const (
	managedVMRootfsMinBytes uint64 = 128 << 20
	managedVMRootfsMaxBytes uint64 = 16 << 40
)

type vmResourceID string

type kernelResourceID string

type cloudInitPresence uint8

const (
	cloudInitAbsent cloudInitPresence = iota + 1
	cloudInitPresent
)

type vmLaunchResourceSpec struct {
	vmID                vmResourceID
	kernelID            kernelResourceID
	expectedRootfsBytes uint64
	cloudInit           cloudInitPresence
}

func newVMLaunchResourceSpec(
	vmID string,
	kernelID string,
	expectedRootfsBytes uint64,
	cloudInit cloudInitPresence,
) (vmLaunchResourceSpec, error) {
	spec := vmLaunchResourceSpec{
		vmID:                vmResourceID(vmID),
		kernelID:            kernelResourceID(kernelID),
		expectedRootfsBytes: expectedRootfsBytes,
		cloudInit:           cloudInit,
	}
	if err := validateVMLaunchResourceSpec(spec); err != nil {
		return vmLaunchResourceSpec{}, err
	}
	return spec, nil
}

func validateVMLaunchResourceSpec(spec vmLaunchResourceSpec) error {
	if !isCanonicalManagedResourceID(string(spec.vmID), 32) {
		return managedVMResourceValidationError(
			"managed VM ID must be 32 lowercase hexadecimal characters",
		)
	}
	if !isCanonicalManagedResourceID(string(spec.kernelID), 64) {
		return managedVMResourceValidationError(
			"managed kernel ID must be 64 lowercase hexadecimal characters",
		)
	}
	if spec.expectedRootfsBytes < managedVMRootfsMinBytes ||
		spec.expectedRootfsBytes > managedVMRootfsMaxBytes {
		return managedVMResourceValidationError("managed VM rootfs size is outside the supported range")
	}
	switch spec.cloudInit {
	case cloudInitAbsent, cloudInitPresent:
		return nil
	default:
		return managedVMResourceValidationError("managed cloud-init presence is invalid")
	}
}

func isCanonicalManagedResourceID(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for index := 0; index < len(value); index++ {
		if (value[index] < '0' || value[index] > '9') && (value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}

func managedVMResourceValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message)
}
