package jailer

import "strings"

// The largest reviewed MMIO pool bounds protocol allocation; actual admission subtracts all devices.
const managedMMIOMaxInterruptSlots = 96

// vmFirecrackerDeviceProfile binds the transport-specific allocator model to an admitted release.
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
	if err := validateVMVolumeResourceSelection(intent.volumes); err != nil {
		return err
	}
	if intent.deviceProfile == nil {
		if len(intent.volumes) != 0 || intent.pciEnabled || intent.nestedVirt {
			return managedVMResourceValidationError("managed devices require an admitted release device profile")
		}
		return nil
	}
	if err := validateManagedDeviceRelease(intent.deviceProfile.release); err != nil {
		return err
	}
	if intent.pciEnabled {
		return validateManagedPCIDeviceBudget(intent)
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

func validateManagedPCIDeviceBudget(intent vmFirecrackerLaunchIntent) error {
	return validateManagedPCITopology(intent.deviceProfile.release.architecture,
		intent.cloudInit == cloudInitPresent, intent.vsockCID != nil, len(intent.volumes))
}

func validateManagedPCITopology(architecture string, cloudInit, vsock bool, volumeCount int) error {
	if volumeCount < 0 || volumeCount > managedMMIOMaxInterruptSlots {
		return managedVMResourceValidationError("managed PCI volume count is outside the protocol bound")
	}
	// Firecracker 1.16.1 has one 32-slot PCI bus with slot zero reserved for its host bridge.
	// Each native block device has one queue, eth0 two, and vsock three; each also uses a
	// configuration MSI-X vector. PCI device GSIs are separate from legacy MMIO interrupts.
	// https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/pci/bus.rs
	devices, vectors := 2+volumeCount, 2+3+2*volumeCount
	if cloudInit {
		devices++
		vectors += 2
	}
	if vsock {
		devices++
		vectors += 4
	}
	var availableVectors int
	switch architecture {
	case "x86_64":
		availableVectors = 4096 - 24
	case "aarch64":
		availableVectors = 4096 - 96
	default:
		return managedVMResourceValidationError("managed PCI device architecture is unsupported")
	}
	if devices > 32-1 || vectors > availableVectors {
		return managedVMResourceValidationError("managed devices exceed the PCI slot or MSI-X budget")
	}
	return nil
}
