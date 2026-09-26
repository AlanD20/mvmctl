package jailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

const (
	// VMLaunchAction selects the closed schema-3 launch transaction.
	VMLaunchAction = "jailer-vm-launch"
	// VMAbortAction selects verified termination and rollback for one exact VM.
	VMAbortAction = "jailer-vm-abort"
	// VMCleanupAction selects idempotent recovery of one exact VM.
	VMCleanupAction = "jailer-vm-cleanup"
	// VMRemoveAction also invalidates the stopped VM's root-owned traffic grants.
	VMRemoveAction = "jailer-vm-remove"
)

// VMLaunchRequest selects canonical resources and typed guest intent. CacheRoot is only a
// locator; no effect may use it after the receiver pins the managed cache authority.
type VMLaunchRequest struct {
	VMID             string                   `json:"vm_id"`
	CacheRoot        string                   `json:"cache_root"`
	KernelID         string                   `json:"kernel_id"`
	RootfsBytes      uint64                   `json:"rootfs_bytes"`
	Release          ReleaseSlot              `json:"release"`
	NetworkPlacement model.VMNetworkPlacement `json:"network_placement"`
	NetworkID        string                   `json:"network_id"`
	GuestIPv4        string                   `json:"guest_ipv4"`
	GuestMAC         string                   `json:"guest_mac"`
	BootArgs         string                   `json:"boot_args"`
	VCPUs            int                      `json:"vcpus"`
	MemoryMiB        int                      `json:"memory_mib"`
	SMT              bool                     `json:"smt"`
	TrackDirtyPages  bool                     `json:"track_dirty_pages"`
	RootfsCacheType  string                   `json:"rootfs_cache_type"`
	CloudInit        bool                     `json:"cloud_init"`
	Logging          bool                     `json:"logging"`
	LogLevel         string                   `json:"log_level"`
	Metrics          bool                     `json:"metrics"`
	Console          bool                     `json:"console"`
	CPUConfig        *model.CpuConfig         `json:"cpu_config,omitempty"`
	VsockCID         *uint32                  `json:"vsock_cid,omitempty"`
	CgroupLimits     model.VMCgroupLimits     `json:"cgroup_limits"`
	Volumes          []VMLaunchVolume         `json:"volumes,omitempty"`
	PCIEnabled       bool                     `json:"pci_enabled"`
	NestedVirt       bool                     `json:"nested_virt"`
}

// VMLaunchVolume selects one canonical raw file without granting pathname authority.
type VMLaunchVolume struct {
	ID            string             `json:"id"`
	Format        model.VolumeFormat `json:"format"`
	ExpectedBytes uint64             `json:"expected_bytes"`
	ReadOnly      bool               `json:"read_only"`
	CacheType     string             `json:"cache_type"`
}

// UnmarshalJSON requires complete volume intent, including an explicit access selection.
func (volume *VMLaunchVolume) UnmarshalJSON(raw []byte) error {
	type launchVolume VMLaunchVolume
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value launchVolume
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("launch volume has trailing JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range []string{"id", "format", "expected_bytes", "read_only", "cache_type"} {
		field, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return fmt.Errorf("launch volume %s is required", name)
		}
	}
	*volume = VMLaunchVolume(value)
	return volume.Validate()
}

// Validate rejects unsupported representations and incomplete volume intent.
func (volume VMLaunchVolume) Validate() error {
	_, err := newVMVolumeResourceSpec(volume.ID, volume.Format, volume.ExpectedBytes,
		volume.private().access, volume.CacheType)
	return err
}

func (volume VMLaunchVolume) private() vmVolumeResourceSpec {
	access := vmVolumeReadWrite
	if volume.ReadOnly {
		access = vmVolumeReadOnly
	}
	return vmVolumeResourceSpec{
		id: vmVolumeResourceID(volume.ID), expectedBytes: volume.ExpectedBytes,
		access: access, cacheType: volume.CacheType,
	}
}

// UnmarshalJSON rejects omitted/null intent rather than silently accepting zero-valued switches.
func (request *VMLaunchRequest) UnmarshalJSON(raw []byte) error {
	type launchRequest VMLaunchRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value launchRequest
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("launch request has trailing JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range []string{"vm_id", "cache_root", "kernel_id", "rootfs_bytes", "release", "network_placement", "network_id", "guest_ipv4", "guest_mac", "boot_args", "vcpus", "memory_mib", "smt", "track_dirty_pages", "rootfs_cache_type", "cloud_init", "logging", "log_level", "metrics", "console", "cgroup_limits", "pci_enabled", "nested_virt"} {
		field, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return fmt.Errorf("launch request %s is required", name)
		}
	}
	*request = VMLaunchRequest(value)
	return nil
}

// VMLaunchResult is a display projection of the committed root authority, never process-control authority.
type VMLaunchResult struct {
	VMID             string `json:"vm_id"`
	LaunchGeneration uint64 `json:"launch_generation"`
	PID              int    `json:"pid"`
	StartTimeTicks   uint64 `json:"start_time_ticks"`
}

// VMCleanupRequest names only the owner-bound VM whose root authority must be recovered.
type VMCleanupRequest struct {
	VMID string `json:"vm_id"`
}

// VMCleanupResult confirms complete root-authoritative cleanup of the requested VM.
type VMCleanupResult struct {
	VMID    string `json:"vm_id"`
	Cleaned bool   `json:"cleaned"`
}

// Validate enforces receiver resource selection and guest intent without any effect.
func (request VMLaunchRequest) Validate() error {
	if request.NetworkPlacement != model.VMNetworkPlacementManaged {
		return instanceValidationError("VM launch network placement is not an admitted managed variant")
	}
	if request.NestedVirt && (!request.PCIEnabled || request.CPUConfig == nil) {
		return instanceValidationError("nested virtualization requires PCI and explicit CPU configuration")
	}
	if request.NestedVirt && request.Release.Architecture != "x86_64" {
		return instanceValidationError("nested virtualization requires an admitted architecture-specific CPU profile")
	}
	if len(request.Volumes) > managedMMIOMaxInterruptSlots {
		return instanceValidationError("VM launch volume selection exceeds the MMIO allocator capacity")
	}
	volumes := make([]vmVolumeResourceSpec, 0, len(request.Volumes))
	for _, volume := range request.Volumes {
		if err := volume.Validate(); err != nil {
			return err
		}
		volumes = append(volumes, volume.private())
	}
	if err := validateVMVolumeResourceSelection(volumes); err != nil {
		return err
	}
	if !vmIDPattern.MatchString(request.VMID) || !driveIDPattern.MatchString(request.KernelID) ||
		!driveIDPattern.MatchString(request.NetworkID) {
		return instanceValidationError("VM launch canonical identities are invalid")
	}
	if _, err := parseManagedCacheLocator(request.CacheRoot); err != nil {
		return err
	}
	if err := request.Release.Validate(); err != nil {
		return err
	}
	if request.RootfsBytes < managedVMRootfsMinBytes || request.RootfsBytes > managedVMRootfsMaxBytes {
		return instanceValidationError("VM launch rootfs size is outside receiver policy")
	}
	ip, err := netip.ParseAddr(request.GuestIPv4)
	if err != nil || !ip.Is4() || ip.String() != request.GuestIPv4 {
		return instanceValidationError("VM launch guest IPv4 is not canonical")
	}
	mac, err := net.ParseMAC(request.GuestMAC)
	if err != nil || len(mac) != 6 || mac.String() != request.GuestMAC {
		return instanceValidationError("VM launch guest MAC is not canonical")
	}
	if err := validateManagedVMCgroupLimits(request.CgroupLimits); err != nil {
		return err
	}
	if request.BootArgs == "" || strings.ContainsRune(request.BootArgs, '\x00') ||
		request.VCPUs < infra.VCPUMin || request.VCPUs > infra.VCPUMax ||
		request.MemoryMiB < infra.MemMinMB || request.MemoryMiB > infra.MemMaxMB {
		return instanceValidationError("VM launch machine intent is invalid")
	}
	return nil
}

func (request VMLaunchRequest) launchIntent(tap vmFirecrackerTapIdentity) vmFirecrackerLaunchIntent {
	cloudInit := cloudInitAbsent
	if request.CloudInit {
		cloudInit = cloudInitPresent
	}
	volumes := make([]vmVolumeResourceSpec, 0, len(request.Volumes))
	for _, volume := range request.Volumes {
		volumes = append(volumes, volume.private())
	}
	return vmFirecrackerLaunchIntent{
		bootArgs: request.BootArgs, vcpuCount: request.VCPUs, memSizeMiB: request.MemoryMiB,
		smt: request.SMT, trackDirtyPages: request.TrackDirtyPages, guestMAC: request.GuestMAC,
		tapIdentity: tap, rootfsCacheType: request.RootfsCacheType, cloudInit: cloudInit,
		enableLogging: request.Logging, logLevel: request.LogLevel, enableMetrics: request.Metrics,
		cpuConfig: request.CPUConfig, vsockCID: request.VsockCID, volumes: volumes,
		pciEnabled: request.PCIEnabled, nestedVirt: request.NestedVirt,
	}
}

// Validate binds a launch response to its exact request.
func (result VMLaunchResult) Validate(request VMLaunchRequest) error {
	if result.VMID != request.VMID || result.LaunchGeneration == 0 {
		return instanceValidationError("VM launch response binding is invalid")
	}
	return validateInstanceProcessLocator(
		instanceProcessLocator{pid: result.PID, startTimeTicks: result.StartTimeTicks},
	)
}

// Validate checks the closed cleanup identity.
func (request VMCleanupRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) {
		return instanceValidationError("VM cleanup identity is invalid")
	}
	return nil
}

// Validate binds a cleanup result to the exact completed request.
func (result VMCleanupResult) Validate(request VMCleanupRequest) error {
	if result.VMID != request.VMID || !result.Cleaned {
		return instanceValidationError("VM cleanup response is not complete for its request")
	}
	return nil
}
