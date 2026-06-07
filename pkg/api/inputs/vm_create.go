package inputs

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/cloudinit"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/disk"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/validators"
)

// VMCreateInput matches Python's VMCreateInput dataclass exactly.
type VMCreateInput struct {
	// Required fields (no defaults)
	Name    string
	SSHKeys []string

	// Optional fields with CLI-layer defaults resolved in Build()
	VCPUCount           *int
	MemSizeMib          string
	User                *string
	PCIEnabled          *bool
	NestedVirt          *bool
	CPUTemplate         string // file path to CPU template JSON
	CPUConfig           map[string]any
	EnableConsole       *bool
	EnableLogging       *bool
	EnableMetrics       *bool
	Image               *string
	KernelID            *string
	BinaryID            *string
	DiskSize            string
	RequestedGuestIP    *string
	SkipCINetworkConfig bool
	BootArgs            string
	LSMFlags            string
	NetworkName         *string
	RequestedGuestMAC   *string
	CustomCloudInitConfig *string
	CloudInitMode       *string
	CloudInitISOPath    *string
	KeepCloudInitISO    bool
	NocloudNetPort      *int
	NoConsole           bool // inverse of EnableConsole, kept for CLI compat
	SkipCleanup         bool
	SkipDeblob          bool
	Count               *int
	Atomic              bool
	Volumes             []string
}

// ResolvedVMCreateInput is the immutable output of VMCreateRequest.Resolve().
type ResolvedVMCreateInput struct {
	Name                string
	VMID                string
	VMDir               string
	VCPUCount           int
	MemSizeMib          int
	User                string
	DNSServer           string
	RootUID             int
	RootGID             int
	UserUID             int
	UserGID             int
	GuestMACPrefix      string
	Network             *model.Network
	Image               *model.ImageItem
	Kernel              *model.KernelItem
	Binary              *model.BinaryItem
	NetworkPrefixLen    int
	CloudInitMode       model.CloudInitMode
	SkipCINetworkConfig bool
	PCIEnabled          bool
	NestedVirt          bool
	EnableConsole       bool
	EnableLogging       bool
	EnableMetrics       bool
	KeepCloudInitISO    bool
	SkipCleanup         bool
	SkipDeblob          bool
	NetworkNetmask      string
	DiskSizeBytes       int64
	DiskSizeMib         int
	LSMFlags            string

	// Firecracker
	LogLevel              string
	LogFilename           string
	SerialOutputFilename  string
	MetricsFilename       string
	APISocketFilename     string
	PIDFilename           string
	ConfigFilename        string
	ConsoleSocketFilename string
	ConsolePIDFilename    string

	// Cloud-init
	CloudInitISOName      string
	NocloudPortRangeStart int
	NocloudPortRangeEnd   int
	NocloudMaxPortRetries int

	RequestedGuestIP   *string
	RequestedGuestMAC  *string
	NocloudNetPort          *int
	CustomCloudInitConfig   *string
	CloudInitISOPath        *string

	// Pre-allocated nocloud server (shared across batch VMs).
	// Set by VMCreate() before the batch loop; flows through CloneVMInput to each VM.
	NoCloudURL          string
	NoCloudPort         int
	NoCloudPID          int
	NoCloudKillAfter    time.Duration
	NoCloudSharedDir    string // shared batch directory for nocloud files
	CPUConfig          *model.CpuConfig
	BootArgs           string
	SSHKeys            []*model.SSHKeyItem
	Provisioner        model.ProvisionerType
	ExtraDrives        []model.DriveConfig
	Volumes            []*model.VolumeItem
}

// VMCreateRequest resolves all DB-backed defaults and validates VM creation inputs.
// Matches Python's src/mvmctl/api/inputs/_vm_create_input.py VMCreateRequest exactly.
type VMCreateRequest struct {
	input  VMCreateInput
	vmID   string
	vmDir  string
	cfg    *config.Service
	vmRepo vm.Repository

	// Resolvers (created in constructor, matching Python's __init__)
	imageResolver   *image.Resolver
	kernelResolver  *kernel.Resolver
	networkResolver *network.Resolver
	binaryResolver  *binary.Resolver
	keyResolver     *key.Resolver
	volumeResolver  *volume.Resolver
	leaseRepo       network.LeaseRepository
}

// NewVMCreateRequest creates a new VMCreateRequest with its own sub-resolvers.
// Matches Python's VMCreateRequest.__init__() exactly.
func NewVMCreateRequest(
	vmID, vmDir string,
	input VMCreateInput,
	cfg *config.Service,
	vmRepo vm.Repository,
	networkRepo network.Repository,
	imageRepo image.Repository,
	kernelRepo kernel.Repository,
	binaryRepo binary.Repository,
	keyRepo key.Repository,
	volumeRepo volume.Repository,
	leaseRepo network.LeaseRepository,
) *VMCreateRequest {
	return &VMCreateRequest{
		input:           input,
		vmID:            vmID,
		vmDir:           vmDir,
		cfg:             cfg,
		vmRepo:          vmRepo,
		imageResolver:   image.NewResolver(imageRepo),
		kernelResolver:  kernel.NewResolver(kernelRepo, nil),
		networkResolver: network.NewResolver(networkRepo, nil),
		binaryResolver:  binary.NewResolver(binaryRepo),
		keyResolver:     key.NewResolver(keyRepo),
		volumeResolver:  volume.NewResolver(volumeRepo),
		leaseRepo:       leaseRepo,
	}
}

// CloneVMInput returns a copy of the resolved result with per-VM fields replaced.
// Matches Python's dataclasses.replace(resolved, name=name, vm_id=vm_id, vm_dir=vm_dir).
func (r *VMCreateRequest) CloneVMInput(resolved *ResolvedVMCreateInput, name, vmID, vmDir string) *ResolvedVMCreateInput {
	if resolved == nil {
		return nil
	}
	cp := *resolved
	cp.Name = name
	cp.VMID = vmID
	cp.VMDir = vmDir
	return &cp
}

// resolveImage resolves the image from a selector or gets the default.
// Matches Python's VMCreateRequest._resolve_image().
func (r *VMCreateRequest) resolveImage(ctx context.Context, input *VMCreateInput) (*model.ImageItem, error) {
	var img *model.ImageItem
	var err error
	if input.Image == nil {
		img, err = r.imageResolver.GetDefault(ctx)
	} else {
		img, err = r.imageResolver.Resolve(ctx, *input.Image)
	}
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMImageNotFound,
			Op:      "vm_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}
	if img == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMImageNotFound,
			Op:      "vm_create",
			Message: "No image specified and no default image set. Use 'mvm image pull <name>' then 'mvm image default <name>', or pass --image.",
			Class:   errs.ClassValidation,
		}
	}
	return img, nil
}

// resolveKernel resolves the kernel from a selector or gets the default.
// Matches Python's VMCreateRequest._resolve_kernel().
func (r *VMCreateRequest) resolveKernel(ctx context.Context, input *VMCreateInput) (*model.KernelItem, error) {
	var krnl *model.KernelItem
	var err error
	if input.KernelID == nil {
		krnl, err = r.kernelResolver.GetDefault(ctx)
	} else {
		krnl, err = r.kernelResolver.Resolve(ctx, *input.KernelID)
	}
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMKernelNotFound,
			Op:      "vm_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}
	if krnl == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMKernelNotFound,
			Op:      "vm_create",
			Message: "No kernel specified and no default kernel set. Use 'mvm kernel pull --type <firecracker|official>' then 'mvm kernel default <id>', or pass --kernel.",
			Class:   errs.ClassValidation,
		}
	}
	return krnl, nil
}

// resolveNetwork resolves the network from a selector or gets the default.
// Matches Python's VMCreateRequest._resolve_network().
func (r *VMCreateRequest) resolveNetwork(ctx context.Context, input *VMCreateInput) (*model.Network, error) {
	var netw *model.Network
	var err error
	if input.NetworkName == nil {
		netw, err = r.networkResolver.GetDefault(ctx)
	} else {
		netw, err = r.networkResolver.Resolve(ctx, *input.NetworkName)
	}
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMNetworkNotFound,
			Op:      "vm_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}
	if netw == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMNetworkNotFound,
			Op:      "vm_create",
			Message: "No network specified and no default network set. Use 'mvm network create' then 'mvm network default <id>', or pass --network.",
			Class:   errs.ClassValidation,
		}
	}
	return netw, nil
}

// resolveBinary resolves the firecracker binary by ID or gets default.
// Matches Python's VMCreateRequest._resolve_binary().
func (r *VMCreateRequest) resolveBinary(ctx context.Context, input *VMCreateInput) (*model.BinaryItem, error) {
	var fcBinary *model.BinaryItem
	if input.BinaryID != nil && *input.BinaryID != "" {
		res, err := r.binaryResolver.Resolve(ctx, *input.BinaryID)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMBinaryNotFound,
				Op:      "vm_create",
				Message: err.Error(),
				Class:   errs.ClassValidation,
			}
		}
		fcBinary = res
	} else {
		defaultBin, err := r.binaryResolver.GetDefault(ctx, "firecracker")
		if err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeDatabaseError,
				Op:      "vm_create",
				Message: fmt.Sprintf("Failed to get default binary: %s", err.Error()),
				Class:   errs.ClassInternal,
			}
		}
		fcBinary = defaultBin
	}
	if fcBinary == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMBinaryNotFound,
			Op:      "vm_create",
			Message: "No binary specified and no default binary set. Use 'mvm bin pull <version>' then 'mvm bin default <id>'.",
			Class:   errs.ClassValidation,
		}
	}
	return fcBinary, nil
}

// resolveSSHKeys resolves SSH key names to key items.
// Matches Python's VMCreateRequest._resolve_ssh_keys().
func (r *VMCreateRequest) resolveSSHKeys(ctx context.Context, input *VMCreateInput) ([]*model.SSHKeyItem, error) {
	if len(input.SSHKeys) == 0 {
		defaults, err := r.keyResolver.GetDefaults(ctx)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeDatabaseError,
				Op:      "vm_create",
				Message: fmt.Sprintf("Failed to get default SSH keys: %s", err.Error()),
				Class:   errs.ClassInternal,
			}
		}
		return defaults, nil
	}
	result, err := r.keyResolver.ResolveMany(ctx, input.SSHKeys)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMSSHKeyNotFound,
			Op:      "vm_create",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}
	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMSSHKeyNotFound,
			Op:      "vm_create",
			Message: result.Errors[0],
			Class:   errs.ClassValidation,
		}
	}
	return result.Items, nil
}

// resolveVolumes resolves volume names to VolumeItems.
// Matches Python's VMCreateRequest._resolve_volumes().
func (r *VMCreateRequest) resolveVolumes(ctx context.Context, input *VMCreateInput) ([]*model.VolumeItem, error) {
	if len(input.Volumes) == 0 {
		return nil, nil
	}
	result := r.volumeResolver.ResolveMany(ctx, input.Volumes)
	if len(result.Errors) > 0 && len(result.Volumes) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Op:      "vm_create",
			Message: result.Errors[0],
			Class:   errs.ClassValidation,
		}
	}
	return result.Volumes, nil
}

// resolveMemory resolves mem_size_mib from input or defaults to setting.
// Matches Python's inline mem_size_mib resolution.
func (r *VMCreateRequest) resolveMemory(ctx context.Context, input *VMCreateInput) (int, error) {
	if input.MemSizeMib == "" {
		return r.cfg.GetInt(ctx, "defaults.vm", "mem_size_mib")
	}
	memStr := strings.TrimSpace(input.MemSizeMib)
	if mib, err := strconv.Atoi(memStr); err == nil {
		return mib, nil
	}
	bytes, err := disk.ParseDiskSizeToBytes(memStr)
	if err != nil {
		return 0, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Invalid memory size: %s", memStr),
			Class:   errs.ClassValidation,
		}
	}
	return int(bytes / disk.MebibyteBytes), nil
}

// Resolve resolves all inputs to explicit values and validates.
// Matches Python's VMCreateRequest.resolve() exactly.
func (r *VMCreateRequest) Resolve(ctx context.Context) (*ResolvedVMCreateInput, error) {
	input := &r.input

	// Validate VM name early (matches Python: VMValidator.validate_name)
	if err := validators.VMName(input.Name); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Invalid VM name: %s", err.Error()),
			Class:   errs.ClassValidation,
		}
	}

	// Resolve image, kernel, network, binary, keys, volumes (matches Python's _resolve_* methods)
	img, err := r.resolveImage(ctx, input)
	if err != nil {
		return nil, err
	}

	krnl, err := r.resolveKernel(ctx, input)
	if err != nil {
		return nil, err
	}

	netw, err := r.resolveNetwork(ctx, input)
	if err != nil {
		return nil, err
	}

	fcBinary, err := r.resolveBinary(ctx, input)
	if err != nil {
		return nil, err
	}

	sshKeys, err := r.resolveSSHKeys(ctx, input)
	if err != nil {
		return nil, err
	}

	vols, err := r.resolveVolumes(ctx, input)
	if err != nil {
		return nil, err
	}

	extraDrives := volume.VolumesToDrives(vols)

	// Validate and parse network subnet (matches Python: ipaddress.IPv4Network(network.subnet, strict=False))
	_, ipv4Net, parseErr := net.ParseCIDR(netw.Subnet)
	if parseErr != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "vm_create",
			Message: fmt.Sprintf("Invalid network subnet: %s", netw.Subnet),
			Class:   errs.ClassValidation,
		}
	}
	networkPrefixLen, _ := ipv4Net.Mask.Size()
	networkNetmask := net.IP(ipv4Net.Mask).String()

	// Resolve disk size (matches Python's inline disk_size resolution)
	var rootfsDiskSizeMib int
	if input.DiskSize != "" {
		bytes, err := disk.ParseDiskSizeToBytes(input.DiskSize)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("Invalid disk size: %s", err.Error()),
				Class:   errs.ClassValidation,
			}
		}
		rootfsDiskSizeMib = int(bytes / disk.MebibyteBytes)
	} else {
		rootfsDiskSizeMib = img.MinRootfsSizeMiB
	}
	rootfsDiskSizeBytes := int64(rootfsDiskSizeMib) * disk.MebibyteBytes

	// Resolve mem_size_mib (matches Python's inline mem_size_mib resolution)
	memMib, err := r.resolveMemory(ctx, input)
	if err != nil {
		return nil, err
	}

	// Resolve VCPU count (matches Python: self._inputs.vcpu_count if ... else SettingsService.resolve)
	vcpuCount, _ := r.cfg.GetInt(ctx, "defaults.vm", "vcpu_count")
	if input.VCPUCount != nil && *input.VCPUCount > 0 {
		vcpuCount = *input.VCPUCount
	}

	// Resolve cloud-init mode (matches Python's _resolve_cloud_init_mode)
	ciResult, err := cloudinit.ResolveMode(input.CloudInitMode, input.CloudInitISOPath)
	if err != nil {
		return nil, err
	}
	ciMode := ciResult.Mode

	// Resolve nested_virt (matches Python: self._inputs.nested_virt if ... else SettingsService.resolve)
	nestedVirt, _ := r.cfg.GetBool(ctx, "defaults.vm", "nested_virt")
	if input.NestedVirt != nil {
		nestedVirt = *input.NestedVirt
	}

	// Resolve CPU config (matches Python's inline cpu_config resolution)
	var cpuConfig map[string]any
	if input.CPUConfig != nil {
		cpuConfig = input.CPUConfig
	}
	if input.CPUTemplate != "" {
		if cpuConfig != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: "Cannot specify both --cpu-template and a pre-resolved cpu_config",
				Class:   errs.ClassValidation,
			}
		}
		data, readErr := os.ReadFile(input.CPUTemplate)
		if readErr != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("Cannot read CPU template: %s", readErr.Error()),
				Class:   errs.ClassValidation,
			}
		}
		var parsed any
		if jsonErr := json.Unmarshal(data, &parsed); jsonErr != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("Invalid CPU template JSON: %s", jsonErr.Error()),
				Class:   errs.ClassValidation,
			}
		}
		var ok bool
		cpuConfig, ok = parsed.(map[string]any)
		if !ok {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: "CPU template must be a JSON object",
				Class:   errs.ClassValidation,
			}
		}
	}

	pciEnabled, _ := r.cfg.GetBool(ctx, "defaults.vm", "pci_enabled")
	if input.PCIEnabled != nil {
		pciEnabled = *input.PCIEnabled
	}

	// Resolve boot_args (matches Python: boot_args or default + root=UUID)
	bootArgs := input.BootArgs
	if bootArgs == "" {
		defaultBootArgs, _ := r.cfg.GetString(ctx, "defaults.vm", "boot_args")
		bootArgs = defaultBootArgs + " root=UUID=" + img.FSUUID
	}

	// Resolve lsm_flags (matches Python: lsm_flags or default)
	lsmFlags := input.LSMFlags
	if lsmFlags == "" {
		lsmFlags, _ = r.cfg.GetString(ctx, "defaults.vm", "lsm_flags")
	}

	// Resolve enable_console (matches Python)
	enableConsole, _ := r.cfg.GetBool(ctx, "defaults.vm", "enable_console")
	if input.EnableConsole != nil {
		enableConsole = *input.EnableConsole
	}

	// Resolve enable_logging (matches Python)
	enableLogging, _ := r.cfg.GetBool(ctx, "defaults.vm", "enable_logging")
	if input.EnableLogging != nil {
		enableLogging = *input.EnableLogging
	}

	// Resolve enable_metrics (matches Python)
	enableMetrics, _ := r.cfg.GetBool(ctx, "defaults.vm", "enable_metrics")
	if input.EnableMetrics != nil {
		enableMetrics = *input.EnableMetrics
	}

	// Resolve user (matches Python: self._inputs.user if ... else SettingsService.resolve)
	user, _ := r.cfg.GetString(ctx, "defaults.vm", "ssh_user")
	if input.User != nil && *input.User != "" {
		user = *input.User
	}

	// Resolve config defaults (matches Python's SettingsService.resolve calls)
	dnsServer, _ := r.cfg.GetString(ctx, "defaults.vm", "dns_server")
	rootUID, _ := r.cfg.GetInt(ctx, "defaults.vm", "root_uid")
	rootGID, _ := r.cfg.GetInt(ctx, "defaults.vm", "root_gid")
	userUID, _ := r.cfg.GetInt(ctx, "defaults.vm", "user_uid")
	userGID, _ := r.cfg.GetInt(ctx, "defaults.vm", "user_gid")
	guestMACPrefix, _ := r.cfg.GetString(ctx, "defaults.vm", "guest_mac_prefix")

	logLevel, _ := r.cfg.GetString(ctx, "defaults.firecracker", "log_level")
	logFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "log_filename")
	serialOutputFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "serial_output_filename")
	metricsFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "metrics_filename")
	apiSocketFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "api_socket_filename")
	pidFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "pid_filename")
	configFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "config_filename")
	consoleSocketFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "console_socket_filename")
	consolePIDFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "console_pid_filename")

	ciIsoName, _ := r.cfg.GetString(ctx, "defaults.cloudinit", "iso_name")
	nocloudPortStart, _ := r.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_port_range_start")
	nocloudPortEnd, _ := r.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_port_range_end")
	nocloudMaxRetries, _ := r.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_max_port_retries")
	nocloudKillAfter, _ := r.cfg.GetDuration(ctx, "defaults.cloudinit", "nocloud_kill_after")

	// Build the resolved result (matches Python's ResolvedVMCreateInput construction)
	result := &ResolvedVMCreateInput{
		Name:                input.Name,
		VMID:                r.vmID,
		VMDir:               r.vmDir,
		VCPUCount:           vcpuCount,
		MemSizeMib:          memMib,
		User:                user,
		DNSServer:           dnsServer,
		RootUID:             rootUID,
		RootGID:             rootGID,
		UserUID:             userUID,
		UserGID:             userGID,
		GuestMACPrefix:      guestMACPrefix,
		Network:             netw,
		Image:               img,
		Kernel:              krnl,
		Binary:              fcBinary,
		NetworkPrefixLen:    networkPrefixLen,
		CloudInitMode:       ciMode,
		SkipCINetworkConfig: input.SkipCINetworkConfig,
		PCIEnabled:          pciEnabled,
		NestedVirt:          nestedVirt,
		CPUConfig:           infra.MapToStruct[model.CpuConfig](cpuConfig),
		EnableConsole:       enableConsole,
		EnableLogging:       enableLogging,
		EnableMetrics:       enableMetrics,
		KeepCloudInitISO:    input.KeepCloudInitISO,
		SkipCleanup:         input.SkipCleanup,
		SkipDeblob:          input.SkipDeblob,
		NetworkNetmask:      networkNetmask,
		DiskSizeBytes:       rootfsDiskSizeBytes,
		DiskSizeMib:         rootfsDiskSizeMib,
		LSMFlags:            lsmFlags,
		RequestedGuestIP:    input.RequestedGuestIP,
		RequestedGuestMAC:   input.RequestedGuestMAC,
		NocloudNetPort:      input.NocloudNetPort,
		CustomCloudInitConfig: input.CustomCloudInitConfig,
		CloudInitISOPath:    ciResult.ISOPath,
		BootArgs:            bootArgs,
		SSHKeys:             sshKeys,
		Volumes:             vols,
		ExtraDrives:         extraDrives,
		// Firecracker defaults (matches Python's GetString calls)
		LogLevel:              logLevel,
		LogFilename:           logFilename,
		SerialOutputFilename:  serialOutputFilename,
		MetricsFilename:       metricsFilename,
		APISocketFilename:     apiSocketFilename,
		PIDFilename:           pidFilename,
		ConfigFilename:        configFilename,
		ConsoleSocketFilename: consoleSocketFilename,
		ConsolePIDFilename:    consolePIDFilename,
		// Cloud-init defaults (matches Python's GetString calls)
		CloudInitISOName:      ciIsoName,
		NocloudPortRangeStart: nocloudPortStart,
		NocloudPortRangeEnd:   nocloudPortEnd,
		NocloudMaxPortRetries: nocloudMaxRetries,
		NoCloudKillAfter:      nocloudKillAfter,
	}

	// Validate (matches Python's ensure_validate)
	if err := r.ensureValidate(ctx, result); err != nil {
		return nil, err
	}

	return result, nil
}

// ensureValidate validates resolved dependencies and batch constraints.
// Matches Python's VMCreateRequest.ensure_validate() exactly.
func (r *VMCreateRequest) ensureValidate(ctx context.Context, result *ResolvedVMCreateInput) error {
	if result == nil {
		return &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassInternal,
		}
	}

	if result.RequestedGuestMAC != nil && *result.RequestedGuestMAC != "" {
		if err := validators.MAC(*result.RequestedGuestMAC); err != nil {
			return &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "vm_create",
				Message: err.Error(),
				Class:   errs.ClassValidation,
			}
		}
	}

	if result.RequestedGuestIP != nil && *result.RequestedGuestIP != "" && result.Network != nil {
		if err := validators.IPv4Address(*result.RequestedGuestIP, "Guest IP", true, result.Network.Subnet, result.Network.IPv4Gateway); err != nil {
			return &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "vm_create",
				Message: err.Error(),
				Class:   errs.ClassValidation,
			}
		}
	}

	if result.VCPUCount < infra.VCPUMin || result.VCPUCount > infra.VCPUMax {
		return &errs.DomainError{
			Code: errs.CodeVMCreateFailed,
			Op:   "vm_create",
			Message: fmt.Sprintf(
				"Invalid vcpus=%d: must be between %d and %d",
				result.VCPUCount, infra.VCPUMin, infra.VCPUMax,
			),
			Class: errs.ClassValidation,
		}
	}

	if result.MemSizeMib < infra.MemMinMB || result.MemSizeMib > infra.MemMaxMB {
		return &errs.DomainError{
			Code: errs.CodeVMCreateFailed,
			Op:   "vm_create",
			Message: fmt.Sprintf(
				"Invalid mem_size_mib=%d: must be between %d and %d",
				result.MemSizeMib, infra.MemMinMB, infra.MemMaxMB,
			),
			Class: errs.ClassValidation,
		}
	}

	kernelPath := result.Kernel.Path
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		return &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Kernel not found: %s", kernelPath),
			Class:   errs.ClassValidation,
		}
	}

	binPath := result.Binary.Path
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		return &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Firecracker binary not found: %s", binPath),
			Class:   errs.ClassValidation,
		}
	}
	if err := syscall.Access(binPath, 1); err != nil {
		return &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Firecracker binary not executable: %s", binPath),
			Class:   errs.ClassValidation,
		}
	}

	if result.CustomCloudInitConfig != nil && *result.CustomCloudInitConfig != "" {
		if _, err := os.Stat(*result.CustomCloudInitConfig); os.IsNotExist(err) {
			return &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("Cloud-init config file not found: %s", *result.CustomCloudInitConfig),
				Class:   errs.ClassValidation,
			}
		}
	}

	if result.Image == nil || result.Image.MinRootfsSizeMiB == 0 {
		imageRef := "<default>"
		if r.input.Image != nil {
			imageRef = *r.input.Image
		}
		return &errs.DomainError{
			Code: errs.CodeVMCreateFailed,
			Op:   "vm_create",
			Message: fmt.Sprintf(
				"Image %s is missing minimum_rootfs_size_mib. This image was created with an older version. Re-import the image: mvm image pull <slug> --force",
				imageRef,
			),
			Class: errs.ClassValidation,
		}
	}

	if result.DiskSizeBytes > 0 {
		minRequiredBytes := int64(result.Image.MinRootfsSizeMiB) * disk.MebibyteBytes
		if result.DiskSizeBytes < minRequiredBytes {
			return &errs.DomainError{
				Code: errs.CodeVMCreateFailed,
				Op:   "vm_create",
				Message: fmt.Sprintf(
					"Requested disk size is smaller than minimum required (%d MiB). Use a larger size or choose a different image.",
					result.Image.MinRootfsSizeMiB,
				),
				Class: errs.ClassValidation,
			}
		}
	}

	if result.BootArgs != "" {
		for component := range strings.FieldsSeq(result.BootArgs) {
			if err := validators.BootArgComponent(component, "boot_args"); err != nil {
				return &errs.DomainError{
					Code:    errs.CodeValidationFailed,
					Op:      "vm_create",
					Message: fmt.Sprintf("Invalid boot_args: %s", err.Error()),
					Class:   errs.ClassValidation,
				}
			}
		}
	}

	if result.LSMFlags != "" {
		if err := validators.BootArgComponent(result.LSMFlags, "lsm_flags"); err != nil {
			return &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("Invalid lsm_flags: %s", err.Error()),
				Class:   errs.ClassValidation,
			}
		}
	}

	// Batch validation (matches Python's ensure_validate batch section)
	count := 1
	if r.input.Count != nil {
		count = *r.input.Count
	}
	if count < 1 {
		return &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: "--count must be at least 1",
			Class:   errs.ClassValidation,
		}
	}

	// Check VM limit (matches Python's _execute_create limit check)
	vmCount, err := r.vmRepo.Count(ctx)
	if err != nil {
		return &errs.DomainError{
			Code:    errs.CodeDatabaseError,
			Op:      "vm_create",
			Message: fmt.Sprintf("Failed to count VMs: %s", err.Error()),
			Class:   errs.ClassInternal,
		}
	}
	maxVMs, _ := r.cfg.GetInt(ctx, "settings.vm", "max_vms")
	if vmCount >= maxVMs {
		return &errs.DomainError{
			Code: errs.CodeVMResourceExhausted,
			Op:   "vm_create",
			Message: fmt.Sprintf(
				"VM limit reached (%d). Remove existing VMs before creating new ones.",
				maxVMs,
			),
			Class: errs.ClassValidation,
		}
	}

	if count > 1 {
		if r.input.RequestedGuestIP != nil {
			return &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: "Cannot specify --ip with --count > 1",
				Class:   errs.ClassValidation,
			}
		}
		if r.input.RequestedGuestMAC != nil {
			return &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: "Cannot specify --mac with --count > 1",
				Class:   errs.ClassValidation,
			}
		}

		// Check subnet capacity (matches Python: lease_repo.count_available)
		if result.Network != nil {
			available, availErr := r.leaseRepo.CountAvailable(ctx, result.Network.ID)
			if availErr == nil && count > available {
				return &errs.DomainError{
					Code: errs.CodeNetworkLeaseExhausted,
					Op:   "vm_create",
					Message: fmt.Sprintf(
						"Subnet has only %d IPs available, but %d VMs requested",
						available, count,
					),
					Class: errs.ClassValidation,
				}
			}
		}

		// Check global VM limit for batch (matches Python: current + count > max_vms)
		if vmCount+count > maxVMs {
			return &errs.DomainError{
				Code: errs.CodeVMResourceExhausted,
				Op:   "vm_create",
				Message: fmt.Sprintf(
					"Creating %d VMs would exceed the limit (%d/%d). Remove existing VMs first.",
					count, vmCount, maxVMs,
				),
				Class: errs.ClassValidation,
			}
		}
	}

	return nil
}
