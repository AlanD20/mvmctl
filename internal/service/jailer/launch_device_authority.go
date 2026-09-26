package jailer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

const (
	launchDeviceAuthorityLeaf     = ".mvm-device-authority.json"
	launchDeviceAuthorityMaxBytes = int(managedVMConfigMaxBytes) + 64*1024
)

// vmLaunchDeviceAuthority is written exclusively by the authenticated root child before exec.
// CRITICAL: It describes admitted launch state, not the mutable Firecracker API or caller SQLite.
type vmLaunchDeviceAuthority struct {
	Schema            uint32                   `json:"schema"`
	VMID              string                   `json:"vm_id"`
	OwnerUID          uint32                   `json:"owner_uid"`
	ExecutionUID      uint32                   `json:"execution_uid"`
	ExecutionGID      uint32                   `json:"execution_gid"`
	Config            managedFirecrackerConfig `json:"config"`
	Rootfs            protectedBackingBinding  `json:"rootfs"`
	KernelID          string                   `json:"kernel_id"`
	KernelBacking     protectedBackingBinding  `json:"kernel_backing"`
	CloudInitBacking  *protectedBackingBinding `json:"cloud_init_backing,omitempty"`
	CgroupLimits      model.VMCgroupLimits     `json:"cgroup_limits"`
	NetworkPlacement  model.VMNetworkPlacement `json:"network_placement"`
	NetworkID         string                   `json:"network_id"`
	GuestIPv4         string                   `json:"guest_ipv4"`
	GuestMAC          string                   `json:"guest_mac"`
	HostBootID        string                   `json:"host_boot_id"`
	LaunchGeneration  uint64                   `json:"launch_generation"`
	Release           ReleaseSlot              `json:"release"`
	FirecrackerSHA256 string                   `json:"firecracker_sha256"`
	JailerSHA256      string                   `json:"jailer_sha256"`
	PCIEnabled        bool                     `json:"pci_enabled"`
	NestedVirt        bool                     `json:"nested_virt"`
	CloudInit         bool                     `json:"cloud_init"`
	Vsock             bool                     `json:"vsock"`
	BootArgs          string                   `json:"boot_args"`
	ConfigSHA256      string                   `json:"config_sha256"`
	Volumes           []launchDeviceVolume     `json:"volumes"`
}

type launchDeviceVolume struct {
	GuestDriveID string                  `json:"guest_drive_id"`
	Path         string                  `json:"path"`
	Backing      protectedBackingBinding `json:"backing"`
	Selection    VMLaunchVolume          `json:"selection"`
	DeviceMajor  uint32                  `json:"device_major"`
	DeviceMinor  uint32                  `json:"device_minor"`
	Inode        uint64                  `json:"inode"`
	UID          uint32                  `json:"uid"`
	GID          uint32                  `json:"gid"`
	Mode         uint16                  `json:"mode"`
}

func writeVMLaunchDeviceAuthority(
	ctx context.Context,
	plan launchChildPlan,
	rootFD int,
	configFD int,
	volumeFDs []int,
) (returnErr error) {
	if plan.HostBootID == "" || plan.LaunchGeneration == 0 || len(volumeFDs) != len(plan.Volumes) {
		return instanceValidationError("launch device authority generation is incomplete")
	}
	configBytes, err := readSealedLaunchDeviceConfig(ctx, configFD)
	if err != nil {
		return err
	}
	config, err := decodeManagedFirecrackerConfig(configBytes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(configBytes)
	metadata := vmLaunchDeviceAuthority{
		FirecrackerSHA256: plan.FirecrackerSHA256,
		JailerSHA256:      plan.JailerSHA256,
		Rootfs:            plan.RootfsBacking,
		KernelID:          plan.KernelID,
		KernelBacking:     plan.KernelBacking,
		CloudInitBacking:  plan.CloudInitBacking,
		CgroupLimits:      plan.Limits,
		NetworkPlacement:  plan.NetworkPlacement,
		NetworkID:         plan.NetworkID,
		GuestIPv4:         plan.GuestIPv4,
		GuestMAC:          plan.GuestMAC,
		Schema:            3,
		VMID:              plan.VMID,
		OwnerUID:          plan.OwnerUID,
		ExecutionUID:      plan.UID,
		ExecutionGID:      plan.GID,
		Config:            config,
		HostBootID:        plan.HostBootID,
		LaunchGeneration:  plan.LaunchGeneration,
		Release:           plan.Release,
		PCIEnabled:        plan.PCIEnabled,
		NestedVirt:        plan.NestedVirt,
		CloudInit:         plan.CloudInit,
		Vsock:             config.Vsock != nil,
		BootArgs:          config.BootSource.BootArgs,
		ConfigSHA256:      hex.EncodeToString(digest[:]),
		Volumes:           make([]launchDeviceVolume, 0, len(plan.Volumes)),
	}
	for index, selection := range plan.Volumes {
		identity, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), volumeFDs[index])
		if err != nil {
			return err
		}
		if identity.uid != 0 || identity.size != selection.ExpectedBytes || identity.nlink != 1 {
			return instanceValidationError("launch volume source changed before device authority publication")
		}
		metadata.Volumes = append(metadata.Volumes, launchDeviceVolume{
			GuestDriveID: plan.GuestDriveIDs[index],
			Path:         "/volumes/" + plan.GuestDriveIDs[index] + ".raw",
			Backing:      plan.VolumeBackings[index],
			Selection:    selection,
			DeviceMajor:  identity.deviceMajor,
			DeviceMinor:  identity.deviceMinor,
			Inode:        identity.inode,
			UID:          identity.uid,
			GID:          identity.gid,
			Mode:         identity.mode,
		})
	}
	if err := metadata.validate(); err != nil {
		return err
	}
	if err := metadata.matchesConfig(config); err != nil {
		return err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return managedVMResourceAtomicError("encode launch device authority", err)
	}
	if len(encoded) > launchDeviceAuthorityMaxBytes {
		return instanceValidationError("launch device authority exceeds its bounded record size")
	}
	fd, err := unix.Openat2(rootFD, launchDeviceAuthorityLeaf, &unix.OpenHow{
		Flags: unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Mode:  0600, Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return processAuthorityError(plan.VMID, "create exclusive launch device authority", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	for offset := 0; offset < len(encoded); {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := unix.Write(fd, encoded[offset:])
		if err != nil {
			return processAuthorityError(plan.VMID, "write launch device authority", err)
		}
		if count <= 0 || count > len(encoded)-offset {
			return processAuthorityError(plan.VMID, "write launch device authority", io.ErrNoProgress)
		}
		offset += count
	}
	if err := unix.Fchmod(fd, 0400); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	return unix.Fsync(rootFD)
}

// readVMLaunchDeviceAuthority requires the caller's retained VM lock and verified active root descriptor.
// CRITICAL: The root inode and record owner/generation/release must match before any device decision.
func readVMLaunchDeviceAuthority(
	ctx context.Context,
	record instanceRecord,
	rootFD int,
) (_ vmLaunchDeviceAuthority, returnErr error) {
	if record.active == nil || record.launch == nil ||
		(record.lifecycle != instanceLifecycleActive && record.lifecycle != instanceLifecycleCleaning) {
		return vmLaunchDeviceAuthority{}, instanceStateError(record.vmID, "active device authority is required")
	}
	if err := ctx.Err(); err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	var root unix.Stat_t
	if err := unix.Fstat(rootFD, &root); err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	if root.Mode != unix.S_IFDIR|0755 || root.Uid != 0 || root.Gid != 0 ||
		uint64(root.Dev) != record.active.jailRoot.device || root.Ino != record.active.jailRoot.inode {
		return vmLaunchDeviceAuthority{}, instanceStateError(
			record.vmID,
			"device authority root differs from active identity",
		)
	}
	fd, err := unix.Openat2(rootFD, launchDeviceAuthorityLeaf, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return vmLaunchDeviceAuthority{}, processAuthorityError(record.vmID, "open root launch device authority", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	deps := realManagedFirecrackerConfigDeps()
	before, err := stableManagedVMConfigStat(ctx, deps, fd)
	if err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	if before.mode != unix.S_IFREG|0400 || before.uid != 0 || before.gid != 0 || before.nlink != 1 ||
		before.size == 0 || before.size > uint64(launchDeviceAuthorityMaxBytes) {
		return vmLaunchDeviceAuthority{}, instanceStateError(
			record.vmID,
			"launch device authority file is not root protected",
		)
	}
	data, err := readLaunchDeviceBytes(ctx, fd, int(before.size))
	if err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	after, err := stableManagedVMConfigStat(ctx, deps, fd)
	if err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	if before != after {
		return vmLaunchDeviceAuthority{}, instanceStateError(record.vmID, "launch device authority changed during read")
	}
	var metadata vmLaunchDeviceAuthority
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return vmLaunchDeviceAuthority{}, processAuthorityError(record.vmID, "decode launch device authority", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return vmLaunchDeviceAuthority{}, instanceValidationError("launch device authority contains trailing data")
	}
	if err := metadata.validate(); err != nil {
		return vmLaunchDeviceAuthority{}, err
	}
	if metadata.VMID != record.vmID || metadata.OwnerUID != record.ownerUID ||
		metadata.NetworkPlacement != record.launch.networkPlacement ||
		metadata.ExecutionUID != record.launch.executionIdentity.uid || metadata.ExecutionGID != record.launch.executionIdentity.gid ||
		metadata.HostBootID != record.launch.hostBootID || metadata.LaunchGeneration != record.launchGeneration ||
		metadata.Release.Version != record.launch.release.version ||
		metadata.Release.Architecture != record.launch.release.architecture || metadata.FirecrackerSHA256 != record.launch.release.firecrackerSHA256 || metadata.JailerSHA256 != record.launch.release.jailerSHA256 {
		return vmLaunchDeviceAuthority{}, instanceStateError(record.vmID, "launch device authority binding differs")
	}
	return metadata, nil
}

func (metadata vmLaunchDeviceAuthority) validate() error {
	if err := validateLaunchDevicePlacement(
		metadata.NetworkPlacement, metadata.NetworkID, metadata.GuestIPv4, metadata.GuestMAC,
	); err != nil {
		return err
	}
	if !authorityHashPattern.MatchString(metadata.FirecrackerSHA256) ||
		!authorityHashPattern.MatchString(metadata.JailerSHA256) {
		return instanceValidationError("launch device release hashes are incomplete")
	}
	if metadata.Rootfs.kind != protectedBackingRootfs || metadata.Rootfs.logicalID != metadata.VMID ||
		metadata.Rootfs.ownerUID != metadata.OwnerUID ||
		metadata.Rootfs.bytes < managedVMRootfsMinBytes ||
		metadata.Rootfs.bytes > managedVMRootfsMaxBytes {
		return instanceValidationError("launch protected rootfs metadata differs")
	}
	if metadata.CloudInit != (metadata.CloudInitBacking != nil) {
		return instanceValidationError("launch protected seed metadata differs")
	}
	if metadata.KernelBacking.kind != "kernel" || metadata.KernelBacking.logicalID != metadata.KernelID ||
		metadata.KernelBacking.ownerUID != metadata.OwnerUID {
		return instanceValidationError("launch protected kernel metadata differs")
	}
	if metadata.Schema != 3 || !vmIDPattern.MatchString(metadata.VMID) || metadata.OwnerUID == 0 ||
		metadata.ExecutionUID == 0 || metadata.ExecutionUID == metadata.OwnerUID || metadata.ExecutionGID == 0 ||
		metadata.HostBootID == "" || metadata.LaunchGeneration == 0 ||
		!isCanonicalManagedResourceID(metadata.ConfigSHA256, 64) {
		return instanceValidationError("launch device authority identity is invalid")
	}
	if err := metadata.Release.Validate(); err != nil {
		return err
	}
	if metadata.NestedVirt && (!metadata.PCIEnabled || metadata.Release.Architecture != "x86_64") {
		return instanceValidationError("launch nested virtualization authority is unsupported")
	}
	if metadata.PCIEnabled {
		if err := metadata.admitVolumeCount(len(metadata.Volumes)); err != nil {
			return err
		}
	}
	if len(metadata.Volumes) > managedMMIOMaxInterruptSlots || metadata.Volumes == nil {
		return instanceValidationError("launch device authority volume inventory is invalid")
	}
	volumes := make([]vmVolumeResourceSpec, 0, len(metadata.Volumes))
	for _, volume := range metadata.Volumes {
		if !isCanonicalManagedResourceID(volume.GuestDriveID, 64) ||
			volume.Path != "/volumes/"+volume.GuestDriveID+".raw" ||
			volume.Backing.kind != protectedBackingVolume ||
			volume.Backing.logicalID != volume.Selection.ID ||
			volume.Backing.ownerUID != metadata.OwnerUID ||
			volume.Backing.bytes != volume.Selection.ExpectedBytes {
			return instanceValidationError("launch protected volume metadata differs")
		}
		if err := volume.Selection.Validate(); err != nil {
			return err
		}
		if volume.Inode == 0 || volume.DeviceMajor == 0 && volume.DeviceMinor == 0 {
			return instanceValidationError("launch device authority volume source identity is incomplete")
		}
		if volume.Mode&unix.S_IFMT != unix.S_IFREG || volume.UID != 0 || volume.Mode&07007 != 0 {
			return instanceValidationError("launch device authority volume source mode is not admitted")
		}
		volumes = append(volumes, volume.Selection.private())
	}
	if err := validateVMVolumeResourceSelection(volumes); err != nil {
		return err
	}
	return metadata.matchesConfig(metadata.Config)
}

// admitVolumeCount admits the complete post-operation data-volume inventory, not a delta.
// The retained root record supplies transport and fixed-device counts; the hotplug journal
// must supply the authenticated current volume inventory before proposing its next count.
func (metadata vmLaunchDeviceAuthority) admitVolumeCount(count int) error {
	if !metadata.PCIEnabled || metadata.Release.Version != "1.16.1" {
		return instanceValidationError("live device changes require the admitted Firecracker PCI release")
	}
	return validateManagedPCITopology(metadata.Release.Architecture, metadata.CloudInit, metadata.Vsock, count)
}

func (metadata vmLaunchDeviceAuthority) matchesConfig(config managedFirecrackerConfig) error {
	want := 1 + len(metadata.Volumes)
	if metadata.CloudInit {
		want++
	}
	if len(config.Drives) != want || config.Drives[0].DriveID != "rootfs" ||
		config.Drives[0].PathOnHost != "/rootfs" || !config.Drives[0].IsRootDevice ||
		(config.Vsock != nil) != metadata.Vsock ||
		config.BootSource.BootArgs != metadata.BootArgs {
		return instanceValidationError("launch device authority differs from sealed device topology")
	}
	switch metadata.NetworkPlacement {
	case model.VMNetworkPlacementManaged:
		if len(config.NetworkInterfaces) != 1 ||
			config.NetworkInterfaces[0].IfaceID != "eth0" ||
			config.NetworkInterfaces[0].GuestMAC != metadata.GuestMAC ||
			!managedVMHostDevicePattern.MatchString(config.NetworkInterfaces[0].HostDevName) {
			return instanceValidationError("launch managed endpoint differs from sealed configuration")
		}
	case model.VMNetworkPlacementNetworkless:
		if len(config.NetworkInterfaces) != 0 || config.NetworkInterfaces == nil {
			return instanceValidationError("launch networkless configuration contains a guest NIC")
		}
		for argument := range strings.FieldsSeq(config.BootSource.BootArgs) {
			if strings.HasPrefix(argument, "ip=") || strings.HasPrefix(argument, "ds=") {
				return instanceValidationError("launch networkless configuration contains network boot arguments")
			}
		}
	default:
		return instanceValidationError("launch device network placement is invalid")
	}
	start := 1
	if metadata.CloudInit {
		if config.Drives[1].DriveID != "cloud-init" || !config.Drives[1].IsReadOnly ||
			config.Drives[1].IsRootDevice || config.Drives[1].PathOnHost != "/"+infra.VMCloudInitISOFilename {
			return instanceValidationError("launch cloud-init authority differs from sealed configuration")
		}
		start++
	}
	for index, volume := range metadata.Volumes {
		drive := config.Drives[start+index]
		if drive.DriveID != volume.GuestDriveID || drive.PathOnHost != volume.Path ||
			drive.IsRootDevice || drive.IsReadOnly != volume.Selection.ReadOnly ||
			drive.CacheType != volume.Selection.CacheType || drive.IOEngine != "Sync" || drive.Partuuid != nil {
			return instanceValidationError("launch volume authority differs from sealed configuration")
		}
	}
	return nil
}

func validateLaunchDevicePlacement(
	placement model.VMNetworkPlacement,
	networkID, guestIPv4, guestMAC string,
) error {
	switch placement {
	case model.VMNetworkPlacementManaged:
		if !driveIDPattern.MatchString(networkID) {
			return instanceValidationError("launch managed network identity is invalid")
		}
		ip, err := netip.ParseAddr(guestIPv4)
		if err != nil || !ip.Is4() || ip.String() != guestIPv4 {
			return instanceValidationError("launch managed guest IPv4 is not canonical")
		}
		mac, err := net.ParseMAC(guestMAC)
		if err != nil || len(mac) != 6 || mac.String() != guestMAC {
			return instanceValidationError("launch managed guest MAC is not canonical")
		}
	case model.VMNetworkPlacementNetworkless:
		if networkID != "" || guestIPv4 != "" || guestMAC != "" {
			return instanceValidationError("launch networkless placement contains a managed endpoint")
		}
	default:
		return instanceValidationError("launch device network placement is invalid")
	}
	return nil
}

func readSealedLaunchDeviceConfig(ctx context.Context, fd int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seals, err := unix.FcntlInt(uintptr(fd), unix.F_GET_SEALS, 0)
	if err != nil || seals&managedVMConfigRequiredSeals != managedVMConfigRequiredSeals {
		return nil, managedVMResourceAtomicError("launch device configuration is not sealed", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode != unix.S_IFREG|0444 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 0 ||
		stat.Size < 1 || uint64(stat.Size) > managedVMConfigMaxBytes {
		return nil, instanceValidationError("launch device configuration is not the protected canonical memfd")
	}
	return readLaunchDeviceBytes(ctx, fd, int(stat.Size))
}

func readLaunchDeviceBytes(ctx context.Context, fd, length int) ([]byte, error) {
	data := make([]byte, length)
	for offset := 0; offset < length; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := unix.Pread(fd, data[offset:], int64(offset))
		if err != nil {
			return nil, managedVMResourceAtomicError("read launch device authority", err)
		}
		if count <= 0 || count > length-offset {
			return nil, managedVMResourceAtomicError("read launch device authority", io.ErrUnexpectedEOF)
		}
		offset += count
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var probe [1]byte
	count, err := unix.Pread(fd, probe[:], int64(length))
	if err != nil {
		return nil, managedVMResourceAtomicError("probe launch device authority", err)
	}
	if count != 0 {
		return nil, instanceValidationError("launch device authority grew while reading")
	}
	return data, nil
}
