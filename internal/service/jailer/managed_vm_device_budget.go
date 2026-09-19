package jailer

import "strings"

// The largest reviewed MMIO pool bounds protocol allocation; actual admission subtracts all devices.
const managedMMIOMaxInterruptSlots = 96

// vmFirecrackerDeviceProfile binds the reviewed allocator model to an admitted release.
// CRITICAL: This is MMIO authority only. PCI needs its own reviewed device and lifecycle policy.
type vmFirecrackerDeviceProfile struct {
	release releaseIdentity
}

func (prepared *preparedRelease) deviceProfile() (*vmFirecrackerDeviceProfile, error) {
	if prepared == nil || prepared.state == nil {
		return nil, managedVMResourceAtomicError("managed device profile requires an active release", nil)
	}
	snapshot := prepared.snapshot()
	if snapshot.status != preparedReleaseOpen || !snapshot.releaseLockActive || !snapshot.hasExecutables {
		return nil, managedVMResourceAtomicError("managed device profile requires a retained admitted release", nil)
	}
	if err := validateManagedDeviceRelease(snapshot.identity); err != nil {
		return nil, err
	}
	return &vmFirecrackerDeviceProfile{release: snapshot.identity}, nil
}

func validateManagedDeviceRelease(release releaseIdentity) error {
	if err := validateReleaseIdentityValue(release); err != nil {
		return managedVMResourceValidationError("managed device profile release is invalid")
	}
	if release.version != "1.16.1" {
		return managedVMResourceValidationError("managed device budget is not approved for this Firecracker release")
	}
	return nil
}

func validateVMFirecrackerDeviceBudget(intent vmFirecrackerLaunchIntent) error {
	if intent.pciEnabled {
		return managedVMResourceValidationError("managed PCI device admission is not approved")
	}
	if err := validateVMVolumeResourceSelection(intent.volumes); err != nil {
		return err
	}
	if intent.deviceProfile == nil {
		if len(intent.volumes) != 0 {
			return managedVMResourceValidationError("managed data volumes require an admitted release device profile")
		}
		return nil
	}
	if err := validateManagedDeviceRelease(intent.deviceProfile.release); err != nil {
		return err
	}
	// Firecracker 1.16.1 reserves one legacy GSI each for VMGenID and VMClock.
	// Every generated MMIO virtio device uses one more, including rootfs and eth0.
	used := 2 + 1 + 1
	var available int
	switch intent.deviceProfile.release.architecture {
	case "x86_64":
		available = 19
	case "aarch64":
		available = managedMMIOMaxInterruptSlots
		used++ // ARM RTC shares the allocator pool.
		if strings.Contains(intent.bootArgs, "console=") {
			used++ // Firecracker creates ARM serial when this substring is present.
		}
	default:
		return managedVMResourceValidationError("managed MMIO device architecture is unsupported")
	}
	if intent.cloudInit == cloudInitPresent {
		used++
	}
	if intent.vsockCID != nil {
		used++
	}
	if len(intent.volumes) > available-used {
		return managedVMResourceValidationError("managed data volumes exceed the complete MMIO interrupt budget")
	}
	return nil
}
