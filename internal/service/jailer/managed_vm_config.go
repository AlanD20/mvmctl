package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

const (
	managedVMConfigMemfdName     = "mvm-firecracker-config"
	managedVMConfigMemfdMode     = uint32(0444)
	managedVMConfigMemfdFlags    = unix.MFD_CLOEXEC | unix.MFD_ALLOW_SEALING
	managedVMPersistentMountPath = "/run/mvm"
	managedVMRuntimeMountPath    = managedVMPersistentMountPath + "/runtime"
	managedVMConfigRequiredSeals = unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW |
		unix.F_SEAL_WRITE | unix.F_SEAL_FUTURE_WRITE
	managedVMConfigRequiredStat = managedVMResourceRequiredFileStat | unix.STATX_GID | unix.STATX_MTIME |
		unix.STATX_CTIME
)

var managedVMHostDevicePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// vmFirecrackerLaunchIntent is the complete private authority for the Firecracker JSON currently emitted by mvmctl.
// CRITICAL: Paths are deliberately absent. Root derives every path and admits only the exact resulting configuration.
type vmFirecrackerLaunchIntent struct {
	networkPlacement model.VMNetworkPlacement
	bootArgs         string
	vcpuCount        int
	memSizeMiB       int
	smt              bool
	trackDirtyPages  bool
	guestMAC         string
	tapIdentity      vmFirecrackerTapIdentity
	rootfsCacheType  string
	cloudInit        cloudInitPresence
	enableLogging    bool
	logLevel         string
	enableMetrics    bool
	cpuConfig        *model.CpuConfig
	vsockCID         *uint32
	volumes          []vmVolumeResourceSpec
	deviceProfile    *vmFirecrackerDeviceProfile
	pciEnabled       bool
	nestedVirt       bool
}

// vmFirecrackerTapIdentity is deliberately not constructible from the wire. The root topology authority supplies
// this private value after deriving the interface identity from its admitted network and VM identities.
type vmFirecrackerTapIdentity struct {
	name string
}

type managedFirecrackerConfig struct {
	BootSource        managedFirecrackerBootSource         `json:"boot-source"`
	Drives            []managedFirecrackerDrive            `json:"drives"`
	NetworkInterfaces []managedFirecrackerNetworkInterface `json:"network-interfaces"`
	MachineConfig     managedFirecrackerMachineConfig      `json:"machine-config"`
	Logger            *managedFirecrackerLogger            `json:"logger,omitempty"`
	Metrics           *managedFirecrackerMetrics           `json:"metrics,omitempty"`
	CPUConfig         *model.CpuConfig                     `json:"cpu-config,omitempty"`
	Vsock             *managedFirecrackerVsock             `json:"vsock,omitempty"`
}

type managedFirecrackerBootSource struct {
	BootArgs        string  `json:"boot_args"`
	KernelImagePath string  `json:"kernel_image_path"`
	InitrdPath      *string `json:"initrd_path,omitempty"`
}

type managedFirecrackerDrive struct {
	DriveID      string  `json:"drive_id"`
	PathOnHost   string  `json:"path_on_host"`
	IsRootDevice bool    `json:"is_root_device"`
	IsReadOnly   bool    `json:"is_read_only"`
	Partuuid     *string `json:"partuuid,omitempty"`
	CacheType    string  `json:"cache_type"`
	IOEngine     string  `json:"io_engine"`
}

type managedFirecrackerNetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac"`
	HostDevName string `json:"host_dev_name"`
}

type managedFirecrackerMachineConfig struct {
	VCPUCount       int     `json:"vcpu_count"`
	MemSizeMiB      int     `json:"mem_size_mib"`
	SMT             bool    `json:"smt"`
	TrackDirtyPages bool    `json:"track_dirty_pages"`
	CPUTemplate     *string `json:"cpu_template,omitempty"`
}

type managedFirecrackerLogger struct {
	LogPath       string `json:"log_path"`
	Level         string `json:"level"`
	ShowLevel     bool   `json:"show_level"`
	ShowLogOrigin bool   `json:"show_log_origin"`
}

type managedFirecrackerMetrics struct {
	MetricsPath string `json:"metrics_path"`
}

type managedFirecrackerVsock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type managedVMConfigStat struct {
	deviceMajor uint32
	deviceMinor uint32
	inode       uint64
	mode        uint16
	uid         uint32
	gid         uint32
	nlink       uint32
	size        uint64
	mtime       unix.StatxTimestamp
	ctime       unix.StatxTimestamp
}

type managedFirecrackerConfigDeps struct {
	memfdCreate func(context.Context, string, int) (int, error)
	pread       func(context.Context, int, []byte, int64) (int, error)
	pwrite      func(context.Context, int, []byte, int64) (int, error)
	statx       func(context.Context, int, int, int, *unix.Statx_t) error
	fstat       func(context.Context, int, *unix.Stat_t) error
	fchown      func(context.Context, int, int, int) error
	fchmod      func(context.Context, int, uint32) error
	seek        func(context.Context, int, int64, int) (int64, error)
	getFDFlags  func(context.Context, int) (int, error)
	addSeals    func(context.Context, int, int) error
	getSeals    func(context.Context, int) (int, error)
	close       func(context.Context, int) error
}

// preparedFirecrackerConfig is the sole retained authority for launch configuration bytes. It intentionally exposes
// no descriptor, path, byte slice, or generic operation until a named mount/launch consumer is implemented.
type preparedFirecrackerConfig struct {
	state *preparedFirecrackerConfigState
}

type preparedFirecrackerConfigState struct {
	// mu guards the descriptor and the cached checked-close result shared by every copied capability handle.
	mu sync.Mutex

	close func(context.Context, int) error
	fd    int

	released   bool
	releaseErr error
}

func validateVMFirecrackerLaunchIntent(intent vmFirecrackerLaunchIntent) error {
	_, err := marshalVMFirecrackerLaunchIntent(intent)
	return err
}

func marshalVMFirecrackerLaunchIntent(intent vmFirecrackerLaunchIntent) ([]byte, error) {
	if intent.bootArgs == "" || strings.ContainsRune(intent.bootArgs, '\x00') {
		return nil, managedVMResourceValidationError("managed Firecracker boot arguments are invalid")
	}
	if intent.vcpuCount < infra.VCPUMin || intent.vcpuCount > infra.VCPUMax {
		return nil, managedVMResourceValidationError("managed Firecracker VCPU count is outside the supported range")
	}
	if intent.memSizeMiB < infra.MemMinMB || intent.memSizeMiB > infra.MemMaxMB {
		return nil, managedVMResourceValidationError("managed Firecracker memory is outside the supported range")
	}
	switch intent.networkPlacement {
	case model.VMNetworkPlacementManaged:
		mac, err := net.ParseMAC(intent.guestMAC)
		if err != nil || len(mac) != 6 || mac.String() != intent.guestMAC {
			return nil, managedVMResourceValidationError("managed Firecracker guest MAC is not canonical")
		}
		if !managedVMHostDevicePattern.MatchString(intent.tapIdentity.name) {
			return nil, managedVMResourceValidationError("managed Firecracker TAP identity is invalid")
		}
	case model.VMNetworkPlacementNetworkless:
		if intent.guestMAC != "" || intent.tapIdentity.name != "" {
			return nil, managedVMResourceValidationError("networkless Firecracker intent contains a managed endpoint")
		}
		for argument := range strings.FieldsSeq(intent.bootArgs) {
			if strings.HasPrefix(argument, "ip=") || strings.HasPrefix(argument, "ds=") {
				return nil, managedVMResourceValidationError(
					"networkless Firecracker boot arguments configure networking",
				)
			}
		}
	default:
		return nil, managedVMResourceValidationError("Firecracker network placement is invalid")
	}
	if intent.rootfsCacheType != model.CacheTypeUnsafe && intent.rootfsCacheType != model.CacheTypeWriteback {
		return nil, managedVMResourceValidationError("managed Firecracker rootfs cache type is invalid")
	}
	switch intent.cloudInit {
	case cloudInitAbsent, cloudInitPresent:
	default:
		return nil, managedVMResourceValidationError("managed cloud-init presence is invalid")
	}
	if intent.enableLogging {
		if !slices.Contains([]string{"Error", "Warning", "Info", "Debug"}, intent.logLevel) {
			return nil, managedVMResourceValidationError("managed Firecracker log level is invalid")
		}
	} else if intent.logLevel != "" {
		return nil, managedVMResourceValidationError("managed Firecracker disabled logger has unexpected intent")
	}
	if intent.vsockCID != nil && *intent.vsockCID < 3 {
		return nil, managedVMResourceValidationError("managed Firecracker vsock CID is invalid")
	}
	if intent.cpuConfig != nil && intent.cpuConfig.KvmCapabilities == nil {
		return nil, managedVMResourceValidationError("managed Firecracker CPU configuration is incomplete")
	}
	if intent.nestedVirt && (!intent.pciEnabled || intent.cpuConfig == nil) {
		return nil, managedVMResourceValidationError(
			"managed nested virtualization requires PCI and explicit CPU configuration",
		)
	}
	if intent.nestedVirt && (intent.deviceProfile == nil || intent.deviceProfile.release.architecture != "x86_64") {
		return nil, managedVMResourceValidationError(
			"managed nested virtualization requires an admitted x86 CPU profile",
		)
	}
	if intent.pciEnabled {
		for argument := range strings.FieldsSeq(intent.bootArgs) {
			if options, present := strings.CutPrefix(argument, "pci="); present {
				for option := range strings.SplitSeq(options, ",") {
					if option == "off" || option == "nomsi" {
						return nil, managedVMResourceValidationError(
							"managed PCI boot arguments disable the selected transport",
						)
					}
				}
			}
		}
	}
	if err := validateVMFirecrackerDeviceBudget(intent); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(buildManagedFirecrackerConfig(intent))
	if err != nil {
		return nil, managedVMResourceAtomicError("encode managed Firecracker launch intent", err)
	}
	if len(canonical) < int(managedVMConfigMinBytes) || len(canonical) > int(managedVMConfigMaxBytes) {
		return nil, managedVMResourceValidationError("managed Firecracker launch intent exceeds configuration policy")
	}
	return canonical, nil
}

func cloneVMFirecrackerLaunchIntent(intent vmFirecrackerLaunchIntent) vmFirecrackerLaunchIntent {
	cloned := intent
	cloned.cpuConfig = cloneManagedCPUConfig(intent.cpuConfig)
	cloned.volumes = slices.Clone(intent.volumes)
	if intent.deviceProfile != nil {
		profile := *intent.deviceProfile
		cloned.deviceProfile = &profile
	}
	if intent.vsockCID != nil {
		cid := *intent.vsockCID
		cloned.vsockCID = &cid
	}
	return cloned
}

func cloneManagedCPUConfig(config *model.CpuConfig) *model.CpuConfig {
	if config == nil {
		return nil
	}
	cloned := *config
	cloned.KvmCapabilities = slices.Clone(config.KvmCapabilities)
	cloned.CpuidModifiers = slices.Clone(config.CpuidModifiers)
	for index := range cloned.CpuidModifiers {
		cloned.CpuidModifiers[index].Modifiers = slices.Clone(config.CpuidModifiers[index].Modifiers)
	}
	cloned.MsrModifiers = slices.Clone(config.MsrModifiers)
	cloned.RegModifiers = slices.Clone(config.RegModifiers)
	cloned.VcpuFeatures = slices.Clone(config.VcpuFeatures)
	return &cloned
}

func buildManagedFirecrackerConfig(intent vmFirecrackerLaunchIntent) managedFirecrackerConfig {
	drives := []managedFirecrackerDrive{{
		DriveID: "rootfs", PathOnHost: "/rootfs", IsRootDevice: true, IsReadOnly: false,
		CacheType: intent.rootfsCacheType, IOEngine: "Sync",
	}}
	if intent.cloudInit == cloudInitPresent {
		drives = append(drives, managedFirecrackerDrive{
			DriveID: "cloud-init", PathOnHost: "/" + infra.VMCloudInitISOFilename,
			IsRootDevice: false, IsReadOnly: true, CacheType: model.CacheTypeUnsafe, IOEngine: "Sync",
		})
	}
	for _, volume := range intent.volumes {
		drives = append(drives, managedFirecrackerDrive{
			DriveID: string(volume.id), PathOnHost: volume.jailPath(),
			IsReadOnly: volume.access == vmVolumeReadOnly,
			CacheType:  volume.cacheType, IOEngine: "Sync",
		})
	}
	config := managedFirecrackerConfig{
		BootSource:        managedFirecrackerBootSource{BootArgs: intent.bootArgs, KernelImagePath: "/kernel"},
		Drives:            drives,
		NetworkInterfaces: []managedFirecrackerNetworkInterface{},
		MachineConfig: managedFirecrackerMachineConfig{
			VCPUCount: intent.vcpuCount, MemSizeMiB: intent.memSizeMiB,
			SMT: intent.smt, TrackDirtyPages: intent.trackDirtyPages,
		},
		CPUConfig: cloneManagedCPUConfig(intent.cpuConfig),
	}
	if intent.networkPlacement == model.VMNetworkPlacementManaged {
		config.NetworkInterfaces = append(config.NetworkInterfaces, managedFirecrackerNetworkInterface{
			IfaceID: "eth0", GuestMAC: intent.guestMAC, HostDevName: intent.tapIdentity.name,
		})
	}
	if intent.enableLogging {
		config.Logger = &managedFirecrackerLogger{
			LogPath: managedVMPersistentMountPath + "/" + infra.VMFirecrackerLogFilename, Level: intent.logLevel,
			ShowLevel: true, ShowLogOrigin: true,
		}
	}
	if intent.enableMetrics {
		config.Metrics = &managedFirecrackerMetrics{
			MetricsPath: managedVMPersistentMountPath + "/" + infra.VMFirecrackerMetricsFilename,
		}
	}
	if intent.vsockCID != nil {
		config.Vsock = &managedFirecrackerVsock{
			GuestCID: *intent.vsockCID, UDSPath: managedVMPrivateRuntimeMountPath + "/" + infra.VMVsockSocketFilename,
		}
	}
	return config
}

// prepareManagedFirecrackerConfig consumes sourceFD on every return path. Caller bytes authorize nothing: they must
// equal the root-built intent, are then discarded, and only a sealed anonymous root-built copy survives.
func prepareManagedFirecrackerConfig(
	ctx context.Context,
	deps managedFirecrackerConfigDeps,
	sourceFD int,
	intent vmFirecrackerLaunchIntent,
) (_ *preparedFirecrackerConfig, returnErr error) {
	if sourceFD < 0 || deps.close == nil {
		return nil, managedVMResourceAtomicError("managed Firecracker configuration source is not active", nil)
	}
	sourceOwned := true
	defer func() {
		if !sourceOwned {
			return
		}
		if closeErr := deps.close(context.WithoutCancel(ctx), sourceFD); closeErr != nil {
			returnErr = appendManagedVMResourceCleanupError(
				returnErr,
				"close managed Firecracker configuration source",
				closeErr,
			)
		}
	}()
	if err := validateManagedFirecrackerConfigDeps(deps); err != nil {
		return nil, err
	}
	canonical, err := marshalVMFirecrackerLaunchIntent(intent)
	if err != nil {
		return nil, err
	}
	callerBytes, err := readStableManagedFirecrackerConfig(ctx, deps, sourceFD)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeManagedFirecrackerConfig(callerBytes)
	if err != nil {
		return nil, err
	}
	decodedCanonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, managedVMResourceAtomicError("encode admitted Firecracker configuration", err)
	}
	if !bytes.Equal(canonical, decodedCanonical) {
		return nil, managedVMResourceValidationError(
			"managed Firecracker configuration does not match the root-built launch intent",
		)
	}
	decoded = managedFirecrackerConfig{}
	callerBytes = nil
	decodedCanonical = nil

	if closeErr := deps.close(context.WithoutCancel(ctx), sourceFD); closeErr != nil {
		sourceOwned = false
		return nil, managedVMResourceCleanupError("close managed Firecracker configuration source", closeErr)
	}
	sourceOwned = false
	if err := ctx.Err(); err != nil {
		return nil, managedVMResourceAtomicError("prepare managed Firecracker configuration", err)
	}
	return createPreparedFirecrackerConfig(ctx, deps, canonical)
}

func validateManagedFirecrackerConfigDeps(deps managedFirecrackerConfigDeps) error {
	if deps.memfdCreate == nil || deps.pread == nil || deps.pwrite == nil || deps.statx == nil || deps.fstat == nil ||
		deps.fchown == nil || deps.fchmod == nil || deps.seek == nil || deps.getFDFlags == nil || deps.addSeals == nil ||
		deps.getSeals == nil || deps.close == nil {
		return managedVMResourceAtomicError("managed Firecracker configuration dependencies are incomplete", nil)
	}
	return nil
}

func readStableManagedFirecrackerConfig(
	ctx context.Context,
	deps managedFirecrackerConfigDeps,
	fd int,
) ([]byte, error) {
	before, err := stableManagedVMConfigStat(ctx, deps, fd)
	if err != nil {
		return nil, err
	}
	// CRITICAL: This fresh bound precedes both allocation and the first read. Earlier pathname admission is not reused.
	if before.size < managedVMConfigMinBytes || before.size > managedVMConfigMaxBytes {
		return nil, managedVMResourceValidationError(
			"managed Firecracker configuration size is outside the supported range",
		)
	}
	data := make([]byte, int(before.size))
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, managedVMResourceAtomicError("read managed Firecracker configuration", err)
		}
		count, readErr := deps.pread(ctx, fd, data[offset:], int64(offset))
		if count < 0 || count > len(data)-offset {
			return nil, managedVMResourceAtomicError(
				"read managed Firecracker configuration",
				fmt.Errorf("invalid positioned read count %d", count),
			)
		}
		offset += count
		if readErr != nil {
			return nil, managedVMResourceAtomicError("read managed Firecracker configuration", readErr)
		}
		if count == 0 {
			return nil, managedVMResourceValidationError("managed Firecracker configuration changed while being read")
		}
	}
	var probe [1]byte
	count, probeErr := deps.pread(ctx, fd, probe[:], int64(len(data)))
	if count < 0 || count > len(probe) {
		return nil, managedVMResourceAtomicError(
			"probe managed Firecracker configuration",
			fmt.Errorf("invalid positioned read count %d", count),
		)
	}
	if probeErr != nil && !errors.Is(probeErr, io.EOF) {
		return nil, managedVMResourceAtomicError("probe managed Firecracker configuration", probeErr)
	}
	if count != 0 {
		return nil, managedVMResourceValidationError("managed Firecracker configuration changed while being read")
	}
	after, err := stableManagedVMConfigStat(ctx, deps, fd)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, managedVMResourceValidationError("managed Firecracker configuration changed while being read")
	}
	return data, nil
}

func stableManagedVMConfigStat(
	ctx context.Context,
	deps managedFirecrackerConfigDeps,
	fd int,
) (managedVMConfigStat, error) {
	if err := ctx.Err(); err != nil {
		return managedVMConfigStat{}, managedVMResourceAtomicError("inspect managed Firecracker configuration", err)
	}
	var stat unix.Statx_t
	if err := deps.statx(ctx, fd, managedVMResourceStatxFlags, managedVMResourceStatxMask, &stat); err != nil {
		return managedVMConfigStat{}, managedVMResourceAtomicError("inspect managed Firecracker configuration", err)
	}
	if stat.Mask&managedVMConfigRequiredStat != managedVMConfigRequiredStat || stat.Ino == 0 ||
		(stat.Dev_major == 0 && stat.Dev_minor == 0) {
		return managedVMConfigStat{}, managedVMResourceValidationError(
			"managed Firecracker configuration has incomplete stable metadata",
		)
	}
	if stat.Attributes_mask&unix.STATX_ATTR_AUTOMOUNT != 0 && stat.Attributes&unix.STATX_ATTR_AUTOMOUNT != 0 {
		return managedVMConfigStat{}, managedVMResourceValidationError(
			"managed Firecracker configuration uses unsupported automount topology",
		)
	}
	return managedVMConfigStat{
		deviceMajor: stat.Dev_major, deviceMinor: stat.Dev_minor, inode: stat.Ino, mode: stat.Mode,
		uid: stat.Uid, gid: stat.Gid, nlink: stat.Nlink, size: stat.Size, mtime: stat.Mtime, ctime: stat.Ctime,
	}, nil
}

func createPreparedFirecrackerConfig(
	ctx context.Context,
	deps managedFirecrackerConfigDeps,
	canonical []byte,
) (_ *preparedFirecrackerConfig, returnErr error) {
	fd, err := deps.memfdCreate(ctx, managedVMConfigMemfdName, managedVMConfigMemfdFlags)
	if err != nil {
		return nil, managedVMResourceAtomicError("create sealed Firecracker configuration", err)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendManagedVMResourceCleanupError(
			returnErr,
			"close rejected sealed Firecracker configuration",
			deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := inspectInitialPreparedFirecrackerConfig(ctx, deps, fd); err != nil {
		return nil, err
	}
	if err := deps.fchown(ctx, fd, 0, 0); err != nil {
		return nil, managedVMResourceAtomicError("set sealed Firecracker configuration owner", err)
	}
	for written := 0; written < len(canonical); {
		if err := ctx.Err(); err != nil {
			return nil, managedVMResourceAtomicError("write sealed Firecracker configuration", err)
		}
		count, writeErr := deps.pwrite(ctx, fd, canonical[written:], int64(written))
		if count < 0 || count > len(canonical)-written {
			return nil, managedVMResourceAtomicError(
				"write sealed Firecracker configuration",
				fmt.Errorf("invalid positioned write count %d", count),
			)
		}
		written += count
		if writeErr != nil {
			return nil, managedVMResourceAtomicError("write sealed Firecracker configuration", writeErr)
		}
		if count == 0 {
			return nil, managedVMResourceAtomicError("write sealed Firecracker configuration", io.ErrNoProgress)
		}
	}
	if err := deps.fchmod(ctx, fd, managedVMConfigMemfdMode); err != nil {
		return nil, managedVMResourceAtomicError("set sealed Firecracker configuration mode", err)
	}
	if err := deps.addSeals(ctx, fd, managedVMConfigRequiredSeals); err != nil {
		return nil, managedVMResourceAtomicError("seal Firecracker configuration", err)
	}
	if err := inspectFinalPreparedFirecrackerConfig(ctx, deps, fd, canonical); err != nil {
		return nil, err
	}
	return &preparedFirecrackerConfig{state: &preparedFirecrackerConfigState{close: deps.close, fd: fd}}, nil
}

func inspectInitialPreparedFirecrackerConfig(
	ctx context.Context,
	deps managedFirecrackerConfigDeps,
	fd int,
) error {
	if err := ctx.Err(); err != nil {
		return managedVMResourceAtomicError("inspect sealed Firecracker configuration", err)
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return managedVMResourceAtomicError("inspect sealed Firecracker configuration", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 0 || stat.Size != 0 {
		return managedVMResourceAtomicError("sealed Firecracker configuration has unsafe initial metadata", nil)
	}
	return nil
}

func inspectFinalPreparedFirecrackerConfig(
	ctx context.Context,
	deps managedFirecrackerConfigDeps,
	fd int,
	canonical []byte,
) error {
	if err := ctx.Err(); err != nil {
		return managedVMResourceAtomicError("inspect sealed Firecracker configuration", err)
	}
	var before unix.Stat_t
	if err := deps.fstat(ctx, fd, &before); err != nil {
		return managedVMResourceAtomicError("inspect sealed Firecracker configuration", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&07777 != managedVMConfigMemfdMode ||
		before.Uid != 0 || before.Gid != 0 || before.Nlink != 0 || before.Size != int64(len(canonical)) {
		return managedVMResourceAtomicError("sealed Firecracker configuration has unsafe final metadata", nil)
	}
	flags, err := deps.getFDFlags(ctx, fd)
	if err != nil {
		return managedVMResourceAtomicError("inspect sealed Firecracker configuration descriptor flags", err)
	}
	if flags != unix.FD_CLOEXEC {
		return managedVMResourceAtomicError("sealed Firecracker configuration is not close-on-exec", nil)
	}
	offset, err := deps.seek(ctx, fd, 0, unix.SEEK_CUR)
	if err != nil {
		return managedVMResourceAtomicError("inspect sealed Firecracker configuration offset", err)
	}
	if offset != 0 {
		return managedVMResourceAtomicError("sealed Firecracker configuration offset is not zero", nil)
	}
	seals, err := deps.getSeals(ctx, fd)
	if err != nil {
		return managedVMResourceAtomicError("inspect Firecracker configuration seals", err)
	}
	if seals&managedVMConfigRequiredSeals != managedVMConfigRequiredSeals {
		return managedVMResourceAtomicError("sealed Firecracker configuration is missing required seals", nil)
	}
	readback := make([]byte, len(canonical))
	for offset := 0; offset < len(readback); {
		if err := ctx.Err(); err != nil {
			return managedVMResourceAtomicError("read back sealed Firecracker configuration", err)
		}
		count, readErr := deps.pread(ctx, fd, readback[offset:], int64(offset))
		if count < 0 || count > len(readback)-offset {
			return managedVMResourceAtomicError(
				"read back sealed Firecracker configuration",
				fmt.Errorf("invalid positioned read count %d", count),
			)
		}
		offset += count
		if readErr != nil {
			return managedVMResourceAtomicError("read back sealed Firecracker configuration", readErr)
		}
		if count == 0 {
			return managedVMResourceAtomicError("read back sealed Firecracker configuration", io.ErrUnexpectedEOF)
		}
	}
	if err := ctx.Err(); err != nil {
		return managedVMResourceAtomicError("probe sealed Firecracker configuration", err)
	}
	var probe [1]byte
	count, readErr := deps.pread(ctx, fd, probe[:], int64(len(readback)))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return managedVMResourceAtomicError("probe sealed Firecracker configuration", readErr)
	}
	if count != 0 || !bytes.Equal(canonical, readback) {
		return managedVMResourceAtomicError("sealed Firecracker configuration readback mismatch", nil)
	}
	if err := ctx.Err(); err != nil {
		return managedVMResourceAtomicError("reinspect sealed Firecracker configuration", err)
	}
	var after unix.Stat_t
	if err := deps.fstat(ctx, fd, &after); err != nil {
		return managedVMResourceAtomicError("reinspect sealed Firecracker configuration", err)
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid ||
		before.Gid != after.Gid || before.Nlink != after.Nlink || before.Size != after.Size ||
		before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return managedVMResourceAtomicError("sealed Firecracker configuration changed during readback", nil)
	}
	return nil
}

func (prepared *preparedFirecrackerConfig) Release(ctx context.Context) error {
	if prepared == nil {
		return nil
	}
	if prepared.state == nil {
		return managedVMResourceAtomicError("sealed Firecracker configuration capability is not active", nil)
	}
	state := prepared.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}

	cleanupCtx := context.Background()
	var result error
	if ctx == nil {
		result = managedVMResourceAtomicError("sealed Firecracker configuration cleanup context is required", nil)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	if state.fd < 0 || state.close == nil {
		result = appendManagedVMResourceCleanupError(
			result,
			"release sealed Firecracker configuration",
			errors.New("sealed Firecracker configuration capability is incomplete"),
		)
	} else if err := state.close(cleanupCtx, state.fd); err != nil {
		result = appendManagedVMResourceCleanupError(
			result,
			"release sealed Firecracker configuration",
			err,
		)
	}
	state.fd = -1
	state.close = nil
	state.released = true
	state.releaseErr = result
	return state.releaseErr
}

func realManagedFirecrackerConfigDeps() managedFirecrackerConfigDeps {
	return managedFirecrackerConfigDeps{
		memfdCreate: func(_ context.Context, name string, flags int) (int, error) {
			return unix.MemfdCreate(name, flags)
		},
		pread: func(_ context.Context, fd int, data []byte, offset int64) (int, error) {
			return unix.Pread(fd, data, offset)
		},
		pwrite: func(_ context.Context, fd int, data []byte, offset int64) (int, error) {
			return unix.Pwrite(fd, data, offset)
		},
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			return unix.Statx(fd, "", flags, mask, stat)
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			return unix.Fstat(fd, stat)
		},
		fchown: func(_ context.Context, fd, uid, gid int) error {
			return unix.Fchown(fd, uid, gid)
		},
		fchmod: func(_ context.Context, fd int, mode uint32) error {
			return unix.Fchmod(fd, mode)
		},
		seek: func(_ context.Context, fd int, offset int64, whence int) (int64, error) {
			return unix.Seek(fd, offset, whence)
		},
		getFDFlags: func(_ context.Context, fd int) (int, error) {
			return unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		},
		addSeals: func(_ context.Context, fd, seals int) error {
			_, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, seals)
			return err
		},
		getSeals: func(_ context.Context, fd int) (int, error) {
			return unix.FcntlInt(uintptr(fd), unix.F_GET_SEALS, 0)
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
	}
}

func decodeManagedFirecrackerConfig(data []byte) (managedFirecrackerConfig, error) {
	if err := validateManagedFirecrackerJSON(data); err != nil {
		return managedFirecrackerConfig{}, managedVMResourceValidationError(
			"managed Firecracker configuration has invalid closed JSON: " + err.Error(),
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config managedFirecrackerConfig
	if err := decoder.Decode(&config); err != nil {
		return managedFirecrackerConfig{}, managedVMResourceValidationError(
			"managed Firecracker configuration has invalid typed JSON: " + err.Error(),
		)
	}
	if err := requireManagedVMConfigEOF(decoder); err != nil {
		return managedFirecrackerConfig{}, err
	}
	return config, nil
}

func requireManagedVMConfigEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return managedVMResourceValidationError("managed Firecracker configuration has trailing JSON")
		}
		return managedVMResourceValidationError("managed Firecracker configuration has invalid trailing JSON")
	}
	return nil
}

type managedVMJSONSchema struct {
	fields  map[string]managedVMJSONField
	element *managedVMJSONSchema
	scalar  bool
}

type managedVMJSONField struct {
	schema   *managedVMJSONSchema
	required bool
}

var managedVMScalarSchema = &managedVMJSONSchema{scalar: true}

func managedVMRequired(schema *managedVMJSONSchema) managedVMJSONField {
	return managedVMJSONField{schema: schema, required: true}
}

func managedVMOptional(schema *managedVMJSONSchema) managedVMJSONField {
	return managedVMJSONField{schema: schema}
}

func managedVMObjectSchema(required []string, optional ...string) *managedVMJSONSchema {
	schema := &managedVMJSONSchema{fields: make(map[string]managedVMJSONField, len(required)+len(optional))}
	for _, field := range required {
		schema.fields[field] = managedVMRequired(managedVMScalarSchema)
	}
	for _, field := range optional {
		schema.fields[field] = managedVMOptional(managedVMScalarSchema)
	}
	return schema
}

func validateManagedFirecrackerJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("configuration is not valid UTF-8")
	}
	modifierSchema := managedVMObjectSchema([]string{"register", "bitmap"})
	cpuConfigSchema := &managedVMJSONSchema{fields: map[string]managedVMJSONField{
		"kvm_capabilities": managedVMRequired(&managedVMJSONSchema{element: managedVMScalarSchema}),
		"cpuid_modifiers": managedVMOptional(&managedVMJSONSchema{element: &managedVMJSONSchema{
			fields: map[string]managedVMJSONField{
				"leaf": managedVMRequired(managedVMScalarSchema), "subleaf": managedVMRequired(managedVMScalarSchema),
				"flags":     managedVMRequired(managedVMScalarSchema),
				"modifiers": managedVMRequired(&managedVMJSONSchema{element: modifierSchema}),
			},
		}}),
		"msr_modifiers": managedVMOptional(&managedVMJSONSchema{
			element: managedVMObjectSchema([]string{"addr", "bitmap"}),
		}),
		"reg_modifiers": managedVMOptional(&managedVMJSONSchema{
			element: managedVMObjectSchema([]string{"addr", "bitmap"}),
		}),
		"vcpu_features": managedVMOptional(&managedVMJSONSchema{
			element: managedVMObjectSchema([]string{"index", "bitmap"}),
		}),
	}}
	schema := &managedVMJSONSchema{fields: map[string]managedVMJSONField{
		"boot-source": managedVMRequired(managedVMObjectSchema(
			[]string{"boot_args", "kernel_image_path"}, "initrd_path",
		)),
		"drives": managedVMRequired(&managedVMJSONSchema{element: managedVMObjectSchema(
			[]string{"drive_id", "path_on_host", "is_root_device", "is_read_only", "cache_type", "io_engine"},
			"partuuid",
		)}),
		"network-interfaces": managedVMRequired(&managedVMJSONSchema{element: managedVMObjectSchema(
			[]string{"iface_id", "guest_mac", "host_dev_name"},
		)}),
		"machine-config": managedVMRequired(managedVMObjectSchema(
			[]string{"vcpu_count", "mem_size_mib", "smt", "track_dirty_pages"}, "cpu_template",
		)),
		"logger": managedVMOptional(
			managedVMObjectSchema([]string{"log_path", "level", "show_level", "show_log_origin"}),
		),
		"metrics":    managedVMOptional(managedVMObjectSchema([]string{"metrics_path"})),
		"cpu-config": managedVMOptional(cpuConfigSchema),
		"vsock":      managedVMOptional(managedVMObjectSchema([]string{"guest_cid", "uds_path"})),
	}}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeManagedVMJSON(decoder, schema, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func consumeManagedVMJSON(decoder *json.Decoder, schema *managedVMJSONSchema, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if token == nil {
		return fmt.Errorf("%s must not be null", path)
	}
	if schema.scalar {
		if _, nested := token.(json.Delim); nested {
			return fmt.Errorf("%s must be a scalar", path)
		}
		return nil
	}
	opening, ok := token.(json.Delim)
	if schema.fields != nil {
		if !ok || opening != '{' {
			return fmt.Errorf("%s must be an object", path)
		}
		seen := make(map[string]struct{}, len(schema.fields))
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return fmt.Errorf("%s: %w", path, keyErr)
			}
			key, keyOK := keyToken.(string)
			if !keyOK {
				return fmt.Errorf("%s has a non-string field", path)
			}
			field, exists := schema.fields[key]
			if !exists {
				return fmt.Errorf("%s has unknown field %q", path, key)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("%s has duplicate field %q", path, key)
			}
			seen[key] = struct{}{}
			if err := consumeManagedVMJSON(decoder, field.schema, path+"."+key); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return fmt.Errorf("%s has invalid object termination", path)
		}
		missing := make([]string, 0)
		for key, field := range schema.fields {
			if field.required {
				if _, exists := seen[key]; !exists {
					missing = append(missing, key)
				}
			}
		}
		if len(missing) != 0 {
			slices.Sort(missing)
			return fmt.Errorf("%s is missing required field %q", path, missing[0])
		}
		return nil
	}
	if !ok || opening != '[' {
		return fmt.Errorf("%s must be an array", path)
	}
	for index := 0; decoder.More(); index++ {
		if err := consumeManagedVMJSON(decoder, schema.element, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return fmt.Errorf("%s has invalid array termination", path)
	}
	return nil
}
