package inputs

import (
	"context"
	"encoding/json"
	"fmt"
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
	"mvmctl/internal/lib/disk"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// VMCreateInput specifies v m create input.
type VMCreateInput struct {
	// Required fields (no defaults)
	Name    string   `json:"name"               yaml:"name"`
	SSHKeys []string `json:"ssh_keys,omitempty" yaml:"ssh_keys,omitempty"`
	// Optional fields with CLI-layer defaults resolved in Build()
	VCPUCount             *int           `json:"vcpu,omitempty"                     yaml:"vcpu,omitempty"`
	MemSizeMib            string         `json:"mem,omitempty"                      yaml:"mem,omitempty"`
	User                  *string        `json:"user,omitempty"                     yaml:"user,omitempty"`
	PCIEnabled            *bool          `json:"pci_enabled,omitempty"              yaml:"pci_enabled,omitempty"`
	NestedVirt            *bool          `json:"nested_virt,omitempty"              yaml:"nested_virt,omitempty"`
	CPUTemplate           string         `json:"cpu_template,omitempty"             yaml:"cpu_template,omitempty"` // file path to CPU template JSON
	CPUConfig             map[string]any `json:"cpu_config,omitempty"               yaml:"cpu_config,omitempty"`
	EnableConsole         *bool          `json:"console_enable,omitempty"           yaml:"console_enable,omitempty"`
	EnableLogging         *bool          `json:"logging_enable,omitempty"           yaml:"logging_enable,omitempty"`
	EnableMetrics         *bool          `json:"metrics_enable,omitempty"           yaml:"metrics_enable,omitempty"`
	ImageID               *string        `json:"image,omitempty"                    yaml:"image,omitempty"`
	KernelID              *string        `json:"kernel,omitempty"                   yaml:"kernel,omitempty"`
	BinaryID              *string        `json:"binary,omitempty"                   yaml:"binary,omitempty"`
	DiskSize              string         `json:"disk_size,omitempty"                yaml:"disk_size,omitempty"`
	RequestedGuestIP      *string        `json:"guest_ip,omitempty"                 yaml:"guest_ip,omitempty"`
	SkipCINetworkConfig   bool           `json:"skip_ci_network_config"             yaml:"skip_ci_network_config"`
	BootArgs              string         `json:"boot_args,omitempty"                yaml:"boot_args,omitempty"`
	LSMFlags              string         `json:"lsm_flags,omitempty"                yaml:"lsm_flags,omitempty"`
	NetworkID             *string        `json:"network,omitempty"                  yaml:"network,omitempty"`
	RequestedGuestMAC     *string        `json:"guest_mac,omitempty"                yaml:"guest_mac,omitempty"`
	CustomCloudInitConfig *string        `json:"custom_cloud_init_config,omitempty" yaml:"custom_cloud_init_config,omitempty"`
	CloudInitMode         *string        `json:"cloud_init_mode,omitempty"          yaml:"cloud_init_mode,omitempty"`
	CloudInitISOPath      *string        `json:"cloud_init_iso_path,omitempty"      yaml:"cloud_init_iso_path,omitempty"`
	KeepCloudInitISO      bool           `json:"keep_cloud_init_iso"                yaml:"keep_cloud_init_iso"`
	NocloudNetPort        *int           `json:"nocloud_net_port,omitempty"         yaml:"nocloud_net_port,omitempty"`
	NoConsole             bool           `json:"no_console"                         yaml:"no_console"` // inverse of EnableConsole, kept for CLI compat
	SkipCleanup           bool           `json:"skip_cleanup"                       yaml:"skip_cleanup"`
	SkipDeblob            bool           `json:"skip_deblob"                        yaml:"skip_deblob"`
	Count                 *int           `json:"count,omitempty"                    yaml:"count,omitempty"`
	Atomic                bool           `json:"atomic"                             yaml:"atomic"`
	Volumes               []string       `json:"volumes,omitempty"                  yaml:"volumes,omitempty"`
	VsockPort             *int           `json:"vsock_port,omitempty"               yaml:"vsock_port,omitempty"`
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
	Network             *model.NetworkItem
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
	VsockFilename         string
	// Cloud-init
	CloudInitISOName      string
	NocloudPortRangeStart int
	NocloudPortRangeEnd   int
	NocloudMaxPortRetries int
	RequestedGuestIP      *string
	RequestedGuestMAC     *string
	NocloudNetPort        *int
	CustomCloudInitConfig *string
	CloudInitISOPath      *string
	// Pre-allocated nocloud server (shared across batch VMs).
	// Set by VMCreate() before the batch loop; flows through CloneVMInput to each VM.
	NoCloudURL       string
	NoCloudPort      int
	NoCloudPID       int
	NoCloudKillAfter time.Duration
	NoCloudSharedDir string // shared batch directory for nocloud files
	CPUConfig        *model.CpuConfig
	BootArgs         string
	SSHKeys          []*model.SSHKeyItem
	Provisioner      model.ProvisionerType
	ExtraDrives      []model.DriveConfig
	Volumes          []*model.VolumeItem
	VsockPort        int // vsock port (0 = disabled / no vsock)
}

// VMCreateRequest resolves all DB-backed defaults and validates VM creation inputs.
type VMCreateRequest struct {
	input  VMCreateInput
	vmID   string
	vmDir  string
	cfg    *config.Service
	vmRepo vm.Repository
	// Resolvers (created in constructor)
	imageResolver   *image.Resolver
	kernelResolver  *kernel.Resolver
	networkResolver *network.Resolver
	binaryResolver  *binary.Resolver
	keyResolver     *key.Resolver
	volumeResolver  *volume.Resolver
	leaseRepo       network.LeaseRepository
}

// NewVMCreateRequest creates a new VMCreateRequest with its own sub-resolvers.
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
func (r *VMCreateRequest) CloneVMInput(
	resolved *ResolvedVMCreateInput,
	name, vmID, vmDir string,
) *ResolvedVMCreateInput {
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
func (r *VMCreateRequest) resolveImage(ctx context.Context, input *VMCreateInput) (*model.ImageItem, error) {
	var img *model.ImageItem
	var err error
	if input.ImageID == nil {
		img, err = r.imageResolver.GetDefault(ctx)
	} else {
		img, err = r.imageResolver.Resolve(ctx, *input.ImageID)
	}
	if err != nil {
		return nil, errs.NotFound(errs.CodeVMImageNotFound, err.Error())
	}
	if img == nil {
		return nil, errs.NotFound(
			errs.CodeVMImageNotFound,
			"No image specified and no default image set. Use 'mvm image pull <name>' then 'mvm image default <name>', or pass --image.",
		)
	}
	return img, nil
}

// resolveKernel resolves the kernel from a selector or gets the default.
func (r *VMCreateRequest) resolveKernel(ctx context.Context, input *VMCreateInput) (*model.KernelItem, error) {
	var krnl *model.KernelItem
	var err error
	if input.KernelID == nil {
		krnl, err = r.kernelResolver.GetDefault(ctx)
	} else {
		krnl, err = r.kernelResolver.Resolve(ctx, *input.KernelID)
	}
	if err != nil {
		return nil, errs.NotFound(errs.CodeVMKernelNotFound, err.Error())
	}
	if krnl == nil {
		return nil, errs.NotFound(
			errs.CodeVMKernelNotFound,
			"No kernel specified and no default kernel set. Use 'mvm kernel pull --type <firecracker|official>' then 'mvm kernel default <id>', or pass --kernel.",
		)
	}
	return krnl, nil
}

// resolveNetwork resolves the network from a selector or gets the default.
func (r *VMCreateRequest) resolveNetwork(ctx context.Context, input *VMCreateInput) (*model.NetworkItem, error) {
	var netw *model.NetworkItem
	var err error
	if input.NetworkID == nil {
		netw, err = r.networkResolver.GetDefault(ctx)
	} else {
		netw, err = r.networkResolver.Resolve(ctx, *input.NetworkID)
	}
	if err != nil {
		return nil, errs.NotFound(errs.CodeVMNetworkNotFound, err.Error())
	}
	if netw == nil {
		return nil, errs.NotFound(
			errs.CodeVMNetworkNotFound,
			"No network specified and no default network set. Use 'mvm network create' then 'mvm network default <id>', or pass --network.",
		)
	}
	return netw, nil
}

// resolveBinary resolves the firecracker binary by ID or gets default.
func (r *VMCreateRequest) resolveBinary(ctx context.Context, input *VMCreateInput) (*model.BinaryItem, error) {
	var fcBinary *model.BinaryItem
	if input.BinaryID != nil && *input.BinaryID != "" {
		res, err := r.binaryResolver.Resolve(ctx, *input.BinaryID)
		if err != nil {
			return nil, errs.NotFound(errs.CodeVMBinaryNotFound, err.Error())
		}
		fcBinary = res
	} else {
		defaultBin, err := r.binaryResolver.GetDefault(ctx, "firecracker")
		if err != nil {
			return nil, errs.New(errs.CodeDatabaseError, fmt.Sprintf("Failed to get default binary: %s", err.Error()))
		}
		fcBinary = defaultBin
	}
	if fcBinary == nil {
		return nil, errs.NotFound(
			errs.CodeVMBinaryNotFound,
			"No binary specified and no default binary set. Use 'mvm bin pull <version>' then 'mvm bin default <id>'.",
		)
	}
	return fcBinary, nil
}

// resolveSSHKeys resolves SSH key names to key items.
func (r *VMCreateRequest) resolveSSHKeys(ctx context.Context, input *VMCreateInput) ([]*model.SSHKeyItem, error) {
	if len(input.SSHKeys) == 0 {
		defaults, err := r.keyResolver.GetDefaults(ctx)
		if err != nil {
			return nil, errs.New(errs.CodeDatabaseError, fmt.Sprintf("Failed to get default SSH keys: %s", err.Error()))
		}
		return defaults, nil
	}
	result, err := r.keyResolver.ResolveMany(ctx, input.SSHKeys)
	if err != nil {
		return nil, errs.NotFound(errs.CodeVMSSHKeyNotFound, err.Error())
	}
	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, errs.NotFound(errs.CodeVMSSHKeyNotFound, result.Errors[0])
	}
	return result.Items, nil
}

// resolveVolumes resolves volume names to VolumeItems.
func (r *VMCreateRequest) resolveVolumes(ctx context.Context, input *VMCreateInput) ([]*model.VolumeItem, error) {
	if len(input.Volumes) == 0 {
		return nil, nil
	}
	result := r.volumeResolver.ResolveMany(ctx, input.Volumes)
	if len(result.Errors) > 0 && len(result.Volumes) == 0 {
		return nil, errs.NotFound(errs.CodeVolumeNotFound, result.Errors[0])
	}
	return result.Volumes, nil
}

// resolveMemory resolves mem_size_mib from input or defaults to setting.
// mem_size_mib resolution.
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
		return 0, errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid memory size: %s", memStr))
	}
	return int(bytes / disk.MebibyteBytes), nil
}

// Resolve resolves all inputs to explicit values and validates.
func (r *VMCreateRequest) Resolve(ctx context.Context) (*ResolvedVMCreateInput, error) {
	input := &r.input
	// Validate VM name early
	if err := validators.VMName(input.Name); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid VM name: %s", err.Error()))
	}
	// Resolve image, kernel, network, binary, keys, volumes
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
	// Validate and parse network subnet.
	_, ipv4Net, parseErr := net.ParseCIDR(netw.Subnet)
	if parseErr != nil {
		return nil, errs.New(errs.CodeNetworkNotFound, fmt.Sprintf("Invalid network subnet: %s", netw.Subnet))
	}
	networkPrefixLen, _ := ipv4Net.Mask.Size()
	networkNetmask := net.IP(ipv4Net.Mask).String()
	// Resolve disk size
	var rootfsDiskSizeMib int
	if input.DiskSize != "" {
		bytes, err := disk.ParseDiskSizeToBytes(input.DiskSize)
		if err != nil {
			return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid disk size: %s", err.Error()))
		}
		rootfsDiskSizeMib = int(bytes / disk.MebibyteBytes)
	} else {
		rootfsDiskSizeMib = img.MinRootfsSizeMiB
	}
	rootfsDiskSizeBytes := int64(rootfsDiskSizeMib) * disk.MebibyteBytes
	// Resolve mem_size_mib
	memMib, err := r.resolveMemory(ctx, input)
	if err != nil {
		return nil, err
	}
	// Resolve VCPU count
	// Validate explicitly set vcpu count before defaulting
	if input.VCPUCount != nil && *input.VCPUCount <= 0 {
		return nil, errs.New(errs.CodeVMCreateFailed,
			"--vcpus must be a positive integer",
			errs.WithClass(errs.ClassValidation),
		)
	}
	vcpuCount, _ := r.cfg.GetInt(ctx, "defaults.vm", "vcpu_count")
	if input.VCPUCount != nil && *input.VCPUCount > 0 {
		vcpuCount = *input.VCPUCount
	}
	// Resolve cloud-init mode
	// Default to "off" when no explicit mode is set — the provisioner injects
	// SSH keys directly into the rootfs regardless of cloud-init mode.
	if input.CloudInitMode == nil && len(sshKeys) > 0 && input.CloudInitISOPath == nil {
		ciStr := string(model.CloudInitModeOFF)
		input.CloudInitMode = &ciStr
	}
	ciResult, err := cloudinit.ResolveMode(input.CloudInitMode, input.CloudInitISOPath)
	if err != nil {
		return nil, err
	}
	ciMode := ciResult.Mode
	// Resolve nested_virt
	nestedVirt, _ := r.cfg.GetBool(ctx, "defaults.vm", "nested_virt")
	if input.NestedVirt != nil {
		nestedVirt = *input.NestedVirt
	}
	// Resolve CPU config
	var cpuConfig map[string]any
	if input.CPUConfig != nil {
		cpuConfig = input.CPUConfig
	}
	if input.CPUTemplate != "" {
		if cpuConfig != nil {
			return nil, errs.New(
				errs.CodeVMCreateFailed,
				"Cannot specify both --cpu-template and a pre-resolved cpu_config",
				errs.WithClass(errs.ClassValidation),
			)
		}
		data, readErr := os.ReadFile(input.CPUTemplate)
		if readErr != nil {
			return nil, errs.New(
				errs.CodeVMCreateFailed,
				fmt.Sprintf("Cannot read CPU template: %s", readErr.Error()),
				errs.WithClass(errs.ClassValidation),
			)
		}
		var parsed any
		if jsonErr := json.Unmarshal(data, &parsed); jsonErr != nil {
			return nil, errs.New(
				errs.CodeVMCreateFailed,
				fmt.Sprintf("Invalid CPU template JSON: %s", jsonErr.Error()),
				errs.WithClass(errs.ClassValidation),
			)
		}
		var ok bool
		cpuConfig, ok = parsed.(map[string]any)
		if !ok {
			return nil, errs.New(
				errs.CodeVMCreateFailed,
				"CPU template must be a JSON object",
				errs.WithClass(errs.ClassValidation),
			)
		}
	}
	pciEnabled, _ := r.cfg.GetBool(ctx, "defaults.vm", "pci_enabled")
	if input.PCIEnabled != nil {
		pciEnabled = *input.PCIEnabled
	}
	// Resolve boot_args
	bootArgs := input.BootArgs
	if bootArgs == "" {
		defaultBootArgs, _ := r.cfg.GetString(ctx, "defaults.vm", "boot_args")
		bootArgs = defaultBootArgs + " root=UUID=" + img.FSUUID
	}
	// Resolve lsm_flags
	lsmFlags := input.LSMFlags
	if lsmFlags == "" {
		lsmFlags, _ = r.cfg.GetString(ctx, "defaults.vm", "lsm_flags")
	}
	// Resolve enable_console
	enableConsole, _ := r.cfg.GetBool(ctx, "defaults.vm", "enable_console")
	if input.EnableConsole != nil {
		enableConsole = *input.EnableConsole
	}
	if input.NoConsole {
		enableConsole = false
	}
	// Resolve enable_logging
	enableLogging, _ := r.cfg.GetBool(ctx, "defaults.vm", "enable_logging")
	if input.EnableLogging != nil {
		enableLogging = *input.EnableLogging
	}
	// Resolve enable_metrics
	enableMetrics, _ := r.cfg.GetBool(ctx, "defaults.vm", "enable_metrics")
	if input.EnableMetrics != nil {
		enableMetrics = *input.EnableMetrics
	}
	// Resolve user
	user, _ := r.cfg.GetString(ctx, "defaults.vm", "ssh_user")
	if input.User != nil && *input.User != "" {
		user = *input.User
	}
	// Resolve config defaults
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
	vsockFilename, _ := r.cfg.GetString(ctx, "defaults.firecracker", "vsock_filename")
	ciIsoName, _ := r.cfg.GetString(ctx, "defaults.cloudinit", "iso_name")
	nocloudPortStart, _ := r.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_port_range_start")
	nocloudPortEnd, _ := r.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_port_range_end")
	nocloudMaxRetries, _ := r.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_max_port_retries")
	nocloudKillAfter, _ := r.cfg.GetDuration(ctx, "defaults.cloudinit", "nocloud_kill_after")
	vsockPort := 0
	if r.input.VsockPort != nil && *r.input.VsockPort > 0 {
		vsockPort = *r.input.VsockPort
	} else {
		vsockPort, _ = r.cfg.GetInt(ctx, "defaults.vm", "vsock_port")
	}
	// Build the resolved result
	result := &ResolvedVMCreateInput{
		Name:                  input.Name,
		VMID:                  r.vmID,
		VMDir:                 r.vmDir,
		VCPUCount:             vcpuCount,
		MemSizeMib:            memMib,
		User:                  user,
		DNSServer:             dnsServer,
		RootUID:               rootUID,
		RootGID:               rootGID,
		UserUID:               userUID,
		UserGID:               userGID,
		GuestMACPrefix:        guestMACPrefix,
		Network:               netw,
		Image:                 img,
		Kernel:                krnl,
		Binary:                fcBinary,
		NetworkPrefixLen:      networkPrefixLen,
		CloudInitMode:         ciMode,
		SkipCINetworkConfig:   input.SkipCINetworkConfig,
		PCIEnabled:            pciEnabled,
		NestedVirt:            nestedVirt,
		CPUConfig:             infra.MapToStruct[model.CpuConfig](cpuConfig),
		EnableConsole:         enableConsole,
		EnableLogging:         enableLogging,
		EnableMetrics:         enableMetrics,
		KeepCloudInitISO:      input.KeepCloudInitISO,
		SkipCleanup:           input.SkipCleanup,
		SkipDeblob:            input.SkipDeblob,
		NetworkNetmask:        networkNetmask,
		DiskSizeBytes:         rootfsDiskSizeBytes,
		DiskSizeMib:           rootfsDiskSizeMib,
		LSMFlags:              lsmFlags,
		RequestedGuestIP:      input.RequestedGuestIP,
		RequestedGuestMAC:     input.RequestedGuestMAC,
		NocloudNetPort:        input.NocloudNetPort,
		CustomCloudInitConfig: input.CustomCloudInitConfig,
		CloudInitISOPath:      ciResult.ISOPath,
		BootArgs:              bootArgs,
		SSHKeys:               sshKeys,
		Volumes:               vols,
		ExtraDrives:           extraDrives,
		// Firecracker defaults
		LogLevel:              logLevel,
		LogFilename:           logFilename,
		SerialOutputFilename:  serialOutputFilename,
		MetricsFilename:       metricsFilename,
		APISocketFilename:     apiSocketFilename,
		PIDFilename:           pidFilename,
		ConfigFilename:        configFilename,
		ConsoleSocketFilename: consoleSocketFilename,
		ConsolePIDFilename:    consolePIDFilename,
		VsockFilename:         vsockFilename,
		// Cloud-init defaults
		CloudInitISOName:      ciIsoName,
		NocloudPortRangeStart: nocloudPortStart,
		NocloudPortRangeEnd:   nocloudPortEnd,
		NocloudMaxPortRetries: nocloudMaxRetries,
		NoCloudKillAfter:      nocloudKillAfter,
		VsockPort:             vsockPort,
	}
	// Validate
	if err := r.ensureValidate(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}

// ensureValidate validates resolved dependencies and batch constraints.
func (r *VMCreateRequest) ensureValidate(ctx context.Context, result *ResolvedVMCreateInput) error {
	if result == nil {
		return errs.New(errs.CodeVMCreateFailed, "Failed to resolve necessary dependencies to validate")
	}
	if result.RequestedGuestMAC != nil && *result.RequestedGuestMAC != "" {
		if err := validators.MAC(*result.RequestedGuestMAC); err != nil {
			return errs.New(errs.CodeValidationFailed, err.Error())
		}
	}
	if result.RequestedGuestIP != nil && *result.RequestedGuestIP != "" && result.Network != nil {
		if err := validators.IPv4Address(
			*result.RequestedGuestIP,
			"Guest IP",
			true,
			result.Network.Subnet,
			result.Network.IPv4Gateway,
		); err != nil {
			return errs.New(errs.CodeValidationFailed, err.Error())
		}
	}
	if result.VCPUCount < infra.VCPUMin || result.VCPUCount > infra.VCPUMax {
		return errs.New(errs.CodeVMCreateFailed,
			fmt.Sprintf("Invalid vcpus=%d: must be between %d and %d", result.VCPUCount, infra.VCPUMin, infra.VCPUMax),
			errs.WithClass(errs.ClassValidation),
		)
	}
	if result.MemSizeMib < infra.MemMinMB || result.MemSizeMib > infra.MemMaxMB {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf(
				"Invalid mem_size_mib=%d: must be between %d and %d",
				result.MemSizeMib,
				infra.MemMinMB,
				infra.MemMaxMB,
			),
			errs.WithClass(errs.ClassValidation),
		)
	}
	kernelPath := result.Kernel.Path
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf("Kernel not found: %s", kernelPath),
			errs.WithClass(errs.ClassValidation),
		)
	}
	binPath := result.Binary.Path
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf("Firecracker binary not found: %s", binPath),
			errs.WithClass(errs.ClassValidation),
		)
	}
	if err := syscall.Access(binPath, 1); err != nil {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf("Firecracker binary not executable: %s", binPath),
			errs.WithClass(errs.ClassValidation),
		)
	}
	if result.CustomCloudInitConfig != nil && *result.CustomCloudInitConfig != "" {
		if _, err := os.Stat(*result.CustomCloudInitConfig); os.IsNotExist(err) {
			return errs.New(
				errs.CodeVMCreateFailed,
				fmt.Sprintf("Cloud-init config file not found: %s", *result.CustomCloudInitConfig),
				errs.WithClass(errs.ClassValidation),
			)
		}
	}
	if result.Image == nil || result.Image.MinRootfsSizeMiB == 0 {
		imageRef := "<default>"
		if r.input.ImageID != nil {
			imageRef = *r.input.ImageID
		}
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf(
				"Image %s is missing minimum_rootfs_size_mib. This image was created with an older version. Re-import the image: mvm image pull <slug> --force",
				imageRef,
			),
			errs.WithClass(errs.ClassValidation),
		)
	}
	minRequiredBytes := int64(result.Image.MinRootfsSizeMiB) * disk.MebibyteBytes
	if result.DiskSizeBytes < minRequiredBytes {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf(
				"Requested disk size is smaller than minimum required (%d MiB). Use a larger size or choose a different image.",
				result.Image.MinRootfsSizeMiB,
			),
			errs.WithClass(errs.ClassValidation),
		)
	}
	if result.BootArgs != "" {
		for component := range strings.FieldsSeq(result.BootArgs) {
			if err := validators.BootArgComponent(component, "boot_args"); err != nil {
				return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid boot_args: %s", err.Error()))
			}
		}
	}
	if result.LSMFlags != "" {
		if err := validators.BootArgComponent(result.LSMFlags, "lsm_flags"); err != nil {
			return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid lsm_flags: %s", err.Error()))
		}
	}
	// Batch validation
	count := 1
	if r.input.Count != nil {
		count = *r.input.Count
	}
	if count < 1 {
		return errs.New(errs.CodeVMCreateFailed, "--count must be at least 1", errs.WithClass(errs.ClassValidation))
	}
	// Check VM limit
	vmCount, err := r.vmRepo.Count(ctx)
	if err != nil {
		return errs.New(errs.CodeDatabaseError, fmt.Sprintf("Failed to count VMs: %s", err.Error()))
	}
	maxVMs, _ := r.cfg.GetInt(ctx, "settings.vm", "max_vms")
	if vmCount >= maxVMs {
		return errs.New(errs.CodeVMResourceExhausted,
			fmt.Sprintf("VM limit reached (%d). Remove existing VMs before creating new ones.", maxVMs),
		)
	}
	if count > 1 {
		if r.input.RequestedGuestIP != nil {
			return errs.New(
				errs.CodeVMCreateFailed,
				"Cannot specify --ip with --count > 1",
				errs.WithClass(errs.ClassValidation),
			)
		}
		if r.input.RequestedGuestMAC != nil {
			return errs.New(
				errs.CodeVMCreateFailed,
				"Cannot specify --mac with --count > 1",
				errs.WithClass(errs.ClassValidation),
			)
		}
		// Check subnet capacity
		if result.Network != nil {
			available, availErr := r.leaseRepo.CountAvailable(ctx, result.Network.ID)
			if availErr == nil && count > available {
				return errs.New(errs.CodeNetworkLeaseExhausted,
					fmt.Sprintf("Subnet has only %d IPs available, but %d VMs requested", available, count),
					errs.WithClass(errs.ClassValidation),
				)
			}
		}
		// Check global VM limit for batch
		if vmCount+count > maxVMs {
			return errs.New(
				errs.CodeVMResourceExhausted,
				fmt.Sprintf(
					"Creating %d VMs would exceed the limit (%d/%d). Remove existing VMs first.",
					count,
					vmCount,
					maxVMs,
				),
			)
		}
	}
	return nil
}
