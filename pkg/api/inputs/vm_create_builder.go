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

// VMCreateBuilder resolves all DB-backed defaults and validates VM creation inputs.
// Matches Python mvmctl.api.inputs._vm_create_input.VMCreateRequest.
type VMCreateBuilder struct {
	cfg         *config.Service
	vmRepo      vm.Repository
	networkRepo network.Repository
	imageRepo   image.Repository
	kernelRepo  kernel.Repository
	binaryRepo  binary.Repository
	keyRepo     key.Repository
	volumeRepo  volume.Repository
	leaseRepo   network.LeaseRepository

	networkResolver *network.Resolver
	imageResolver   *image.Resolver
	kernelResolver  *kernel.Resolver
	binaryResolver  *binary.Resolver
	keyResolver     *key.Resolver
	volumeResolver  *volume.Resolver

	vmID  string
	vmDir string
}

// NewVMCreateBuilder creates a new VMCreateBuilder with all resolvers.
// Matches Python's VMCreateRequest.__init__().
func NewVMCreateBuilder(
	cfg *config.Service,
	vmRepo vm.Repository,
	networkRepo network.Repository,
	imageRepo image.Repository,
	kernelRepo kernel.Repository,
	binaryRepo binary.Repository,
	keyRepo key.Repository,
	volumeRepo volume.Repository,
	leaseRepo network.LeaseRepository,
	vmID string,
	vmDir string,
) *VMCreateBuilder {
	return &VMCreateBuilder{
		cfg:             cfg,
		vmRepo:          vmRepo,
		networkRepo:     networkRepo,
		imageRepo:       imageRepo,
		kernelRepo:      kernelRepo,
		binaryRepo:      binaryRepo,
		keyRepo:         keyRepo,
		volumeRepo:      volumeRepo,
		leaseRepo:       leaseRepo,
		networkResolver: network.NewResolver(networkRepo, nil),
		imageResolver:   image.NewResolver(imageRepo),
		kernelResolver:  kernel.NewResolver(kernelRepo, nil),
		binaryResolver:  binary.NewResolver(binaryRepo),
		keyResolver:     key.NewResolver(keyRepo),
		volumeResolver:  volume.NewResolver(volumeRepo),
		vmID:            vmID,
		vmDir:           vmDir,
	}
}

// Build resolves all inputs to explicit values.
// Matches Python's VMCreateRequest.resolve() exactly.
func (b *VMCreateBuilder) Build(ctx context.Context, raw VMCreateInput) (*VMCreateResolved, error) {
	// Validate VM name early — before any DB or subprocess calls
	if err := validators.VMName(raw.Name); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Invalid VM name: %s", err.Error()),
			Class:   errs.ClassValidation,
		}
	}

	// Resolve dependencies
	img, err := b.resolveImage(ctx, raw)
	if err != nil {
		return nil, err
	}

	krnl, err := b.resolveKernel(ctx, raw)
	if err != nil {
		return nil, err
	}

	netw, err := b.resolveNetwork(ctx, raw)
	if err != nil {
		return nil, err
	}

	fcBinary, err := b.resolveBinary(ctx, raw)
	if err != nil {
		return nil, err
	}

	sshKeys, err := b.resolveSSHKeys(ctx, raw)
	if err != nil {
		return nil, err
	}

	vols, err := b.resolveVolumes(ctx, raw)
	if err != nil {
		return nil, err
	}

	// Convert volumes to drive configs
	extraDrives := volume.VolumesToDrives(vols)
	// Validate network subnet is valid CIDR notation
	if _, _, err := net.ParseCIDR(netw.Subnet); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "vm_create",
			Message: fmt.Sprintf("Invalid network subnet: %s", netw.Subnet),
			Class:   errs.ClassValidation,
		}
	}
	// Resolve disk size
	var rootfsDiskSizeMib int
	if raw.DiskSize != "" {
		bytes, err := disk.ParseDiskSizeToBytes(raw.DiskSize)
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

	// Validate VCPU count is provided explicitly
	if raw.VCPUCount == nil || *raw.VCPUCount == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: "VCPU count is required",
			Class:   errs.ClassValidation,
		}
	}

	// Resolve memory
	memMib, err := b.resolveMemory(ctx, raw)
	if err != nil {
		return nil, err
	}

	// Resolve cloud-init mode
	ciResult, err := cloudinit.ResolveMode(raw.CloudInitMode, raw.CloudInitISOPath)
	if err != nil {
		return nil, err
	}
	_ = ciResult

	// Resolve nested_virt
	var nestedVirt bool
	if raw.NestedVirt != nil {
		nestedVirt = *raw.NestedVirt
	} else {
		nestedVirt, _ = b.cfg.GetBool(ctx, "defaults.vm", "nested_virt")
	}

	// Resolve CPU config: from cpu_template (CLI) or cpu_config (import)
	var cpuConfig map[string]any
	if raw.CPUConfig != nil {
		cpuConfig = raw.CPUConfig
	}
	if raw.CPUTemplate != "" {
		if cpuConfig != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: "Cannot specify both --cpu-template and a pre-resolved cpu_config",
				Class:   errs.ClassValidation,
			}
		}
		data, err := os.ReadFile(raw.CPUTemplate)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("Cannot read CPU template: %s", err.Error()),
				Class:   errs.ClassValidation,
			}
		}
		var parsed any
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("Invalid CPU template JSON: %s", err.Error()),
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

	// ── Item 9: CPU config merge with nested_virt (Python _vm_create_input.py:452-458) ──
	if nestedVirt {
		base := map[string]any{"kvm_capabilities": []any{}}
		if cpuConfig != nil {
			cpuConfig = infra.DeepMergeDict(base, cpuConfig)
		} else {
			cpuConfig = base
		}
	}

	// ── Item 10: Nested virt forces PCI on (Python _vm_create_input.py:468-469) ──
	pciEnabled := false
	if raw.PCIEnabled != nil {
		pciEnabled = *raw.PCIEnabled
	} else {
		pciEnabled, _ = b.cfg.GetBool(ctx, "defaults.vm", "pci_enabled")
	}
	if nestedVirt {
		pciEnabled = true
	}

	// ── Item 11: boot_args default with root=UUID (Python _vm_create_input.py:526-528) ──
	bootArgs := raw.BootArgs
	if bootArgs == nil {
		defaultBootArgs, _ := b.cfg.GetString(ctx, "defaults.vm", "boot_args")
		uuidSuffix := img.FSUUID
		bootArgsStr := defaultBootArgs + " root=UUID=" + uuidSuffix
		bootArgs = &bootArgsStr
	}

	// Resolve lsm_flags
	if raw.LSMFlags == "" {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: "lsm_flags is required",
			Class:   errs.ClassValidation,
		}
	}

	// Build the resolved result — matches Python VMCreateRequest.resolve() result construction
	result := &VMCreateResolved{
		Name:                raw.Name,
		Image:               img,
		Kernel:              krnl,
		Network:             netw,
		Binary:              fcBinary,
		SSHKeys:             sshKeys,
		Volumes:             vols,
		ExtraDrives:         extraDrives,
		VCPUCount:           *raw.VCPUCount,
		MemSizeMib:          memMib,
		DiskSizeMib:         rootfsDiskSizeMib,
		DiskSizeBytes:       rootfsDiskSizeBytes,
		NestedVirt:          nestedVirt,
		PCIEnabled:          pciEnabled,
		BootArgs:            bootArgs,
		LSMFlags:            raw.LSMFlags,
		RequestedGuestIP:    raw.RequestedGuestIP,
		RequestedGuestMAC:   raw.RequestedGuestMAC,
		CustomUserDataPath:  raw.CustomUserDataPath,
		CPUConfig:           infra.MapToStruct[model.CpuConfig](cpuConfig),
		SkipCINetworkConfig: raw.SkipCINetworkConfig,
		SkipCleanup:         raw.SkipCleanup,
		SkipDeblob:          raw.SkipDeblob,
	}

	// ── Post-resolution validation (matches Python VMCreateRequest.ensure_validate()) ──

	// Item 8: MAC validation (Python _vm_create_input.py:604-605)
	if result.RequestedGuestMAC != nil && *result.RequestedGuestMAC != "" {
		if err := validators.MAC(*result.RequestedGuestMAC); err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "vm_create",
				Message: err.Error(),
				Class:   errs.ClassValidation,
			}
		}
	}

	// Item 7: Guest IP validation (Python _vm_create_input.py:607-614)
	if result.RequestedGuestIP != nil && *result.RequestedGuestIP != "" && result.Network != nil {
		if err := validators.IPv4Address(*result.RequestedGuestIP, "Guest IP", true, result.Network.Subnet, result.Network.IPv4Gateway); err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "vm_create",
				Message: err.Error(),
				Class:   errs.ClassValidation,
			}
		}
	}

	// Item 1: VCPU range validation (Python _vm_create_input.py:616-621)
	if result.VCPUCount < infra.VCPUMin || result.VCPUCount > infra.VCPUMax {
		return nil, &errs.DomainError{
			Code: errs.CodeVMCreateFailed,
			Op:   "vm_create",
			Message: fmt.Sprintf(
				"Invalid vcpus=%d: must be between %d and %d",
				result.VCPUCount,
				infra.VCPUMin,
				infra.VCPUMax,
			),
			Class: errs.ClassValidation,
		}
	}

	// Item 2: Memory range validation (Python _vm_create_input.py:622-629)
	if result.MemSizeMib < infra.MemMinMB || result.MemSizeMib > infra.MemMaxMB {
		return nil, &errs.DomainError{
			Code: errs.CodeVMCreateFailed,
			Op:   "vm_create",
			Message: fmt.Sprintf(
				"Invalid mem_size_mib=%d: must be between %d and %d",
				result.MemSizeMib,
				infra.MemMinMB,
				infra.MemMaxMB,
			),
			Class: errs.ClassValidation,
		}
	}

	// Item 3: Kernel path existence (Python _vm_create_input.py:631-634)
	kernelPath := result.Kernel.Path
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Kernel not found: %s", kernelPath),
			Class:   errs.ClassValidation,
		}
	}

	// Item 4: Binary executable check (Python _vm_create_input.py:636-638)
	binPath := result.Binary.Path
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Firecracker binary not found: %s", binPath),
			Class:   errs.ClassValidation,
		}
	} else if err := syscall.Access(binPath, 1); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: fmt.Sprintf("Firecracker binary not found: %s", binPath),
			Class:   errs.ClassValidation,
		}
	}

	// Item 5: Custom user data path existence (Python _vm_create_input.py:640-646)
	if result.CustomUserDataPath != nil && *result.CustomUserDataPath != "" {
		if _, err := os.Stat(*result.CustomUserDataPath); os.IsNotExist(err) {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("User-data file not found: %s", *result.CustomUserDataPath),
				Class:   errs.ClassValidation,
			}
		}
	}

	// Item 6: Minimum rootfs size checks (Python _vm_create_input.py:648-669)
	if result.Image == nil || result.Image.MinRootfsSizeMiB == 0 {
		imageRef := "<default>"
		if raw.Image != nil {
			imageRef = *raw.Image
		}
		return nil, &errs.DomainError{
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
			return nil, &errs.DomainError{
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

	// Validate boot_args components
	if result.BootArgs != nil {
		for component := range strings.FieldsSeq(*result.BootArgs) {
			if err := validators.BootArgComponent(component, "boot_args"); err != nil {
				return nil, &errs.DomainError{
					Code:    errs.CodeValidationFailed,
					Op:      "vm_create",
					Message: fmt.Sprintf("Invalid boot_args: %s", err.Error()),
					Class:   errs.ClassValidation,
				}
			}
		}
	}

	// Validate lsm_flags component
	if result.LSMFlags != "" {
		if err := validators.BootArgComponent(result.LSMFlags, "lsm_flags"); err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeValidationFailed,
				Op:      "vm_create",
				Message: fmt.Sprintf("Invalid lsm_flags: %s", err.Error()),
				Class:   errs.ClassValidation,
			}
		}
	}

	// Batch validation — Python: count = self._inputs.count if self._inputs.count is not None else 1
	count := 1
	if raw.Count != nil {
		count = *raw.Count
	}
	if count < 1 {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_create",
			Message: "--count must be at least 1",
			Class:   errs.ClassValidation,
		}
	}

	if count > 1 {
		if raw.RequestedGuestIP != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: "Cannot specify --ip with --count > 1",
				Class:   errs.ClassValidation,
			}
		}
		if raw.RequestedGuestMAC != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Op:      "vm_create",
				Message: "Cannot specify --mac with --count > 1",
				Class:   errs.ClassValidation,
			}
		}

		// Check subnet capacity — Python: lease_repo.count_available(self.result.network.id)
		available := 0
		if result.Network != nil {
			available, _ = b.leaseRepo.CountAvailable(ctx, result.Network.ID)
		}
		if count > available {
			return nil, &errs.DomainError{
				Code:    errs.CodeNetworkLeaseExhausted,
				Op:      "vm_create",
				Message: fmt.Sprintf("Subnet has only %d IPs available, but %d VMs requested", available, count),
				Class:   errs.ClassValidation,
			}
		}

		// Check global VM limit
		current, err := b.vmRepo.Count(ctx)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeDatabaseError,
				Op:      "vm_create",
				Message: fmt.Sprintf("Failed to count VMs: %s", err.Error()),
				Class:   errs.ClassInternal,
			}
		}
		maxVMs, _ := b.cfg.GetInt(ctx, "settings.vm", "max_vms")
		if current+count > maxVMs {
			return nil, &errs.DomainError{
				Code: errs.CodeVMResourceExhausted,
				Op:   "vm_create",
				Message: fmt.Sprintf(
					"Creating %d VMs would exceed the limit (%d/%d). Remove existing VMs first.",
					count,
					current,
					maxVMs,
				),
				Class: errs.ClassValidation,
			}
		}
	}

	return result, nil
}

// ── Private resolution helpers ──────────────────────────────────────────────

func (b *VMCreateBuilder) resolveImage(ctx context.Context, raw VMCreateInput) (*model.ImageItem, error) {
	var img *model.ImageItem
	var err error

	if raw.Image == nil {
		img, err = b.imageResolver.GetDefault(ctx)
	} else {
		img, err = b.imageResolver.Resolve(ctx, *raw.Image)
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

func (b *VMCreateBuilder) resolveKernel(ctx context.Context, raw VMCreateInput) (*model.KernelItem, error) {
	var krnl *model.KernelItem
	var err error

	if raw.KernelID == nil {
		krnl, err = b.kernelResolver.GetDefault(ctx)
	} else {
		krnl, err = b.kernelResolver.Resolve(ctx, *raw.KernelID)
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

func (b *VMCreateBuilder) resolveNetwork(ctx context.Context, raw VMCreateInput) (*model.Network, error) {
	var netw *model.Network
	var err error

	if raw.NetworkName == nil {
		netw, err = b.networkResolver.GetDefault(ctx)
	} else {
		netw, err = b.networkResolver.Resolve(ctx, *raw.NetworkName)
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

func (b *VMCreateBuilder) resolveBinary(ctx context.Context, raw VMCreateInput) (*model.BinaryItem, error) {
	var fcBinary *model.BinaryItem

	if raw.BinaryID != nil && *raw.BinaryID != "" {
		res, err := b.binaryResolver.Resolve(ctx, *raw.BinaryID)
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
		defaultBin, err := b.binaryResolver.GetDefault(ctx, "firecracker")
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

func (b *VMCreateBuilder) resolveSSHKeys(ctx context.Context, raw VMCreateInput) ([]*model.SSHKeyItem, error) {
	if len(raw.SSHKeys) == 0 {
		defaults, err := b.keyRepo.GetDefaults(ctx)
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

	result, err := b.keyResolver.ResolveMany(ctx, raw.SSHKeys)
	if err != nil {
		// Propagate non-not-found errors (database errors, MVMKeyError, etc.)
		// Python: wraps in DomainError for consistency
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

func (b *VMCreateBuilder) resolveVolumes(ctx context.Context, raw VMCreateInput) ([]*model.VolumeItem, error) {
	if len(raw.Volumes) == 0 {
		return nil, nil
	}

	result := b.volumeResolver.ResolveMany(ctx, raw.Volumes)
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

func (b *VMCreateBuilder) resolveMemory(ctx context.Context, raw VMCreateInput) (int, error) {
	if raw.MemSizeMib != "" {
		memStr := strings.TrimSpace(raw.MemSizeMib)
		// Try parsing as raw int first (Python: int(mem_str))
		if mib, err := strconv.Atoi(memStr); err == nil {
			return mib, nil
		}
		// Try parsing as human-readable (512M, 1G) — Python: DiskUtils.parse_disk_size_to_bytes(mem_str)
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

	v, _ := b.cfg.GetInt(ctx, "defaults.vm", "mem_size_mib")
	return v, nil
}

// generateBinaryID generates a content-addressed SHA256 hash for a binary.
// Matches Python's HashGenerator.binary().

// FromVM reconstructs a VMCreateResolved from an enriched VM state.
// Matches Python's VMCreateRequest.from_vm() exactly.
//
// Python raises errors if enriched relations are missing, then resolves
// every VM field through SettingsService for defaults:
//
//	user=vm.ssh_user if vm.ssh_user else str(SettingsService.resolve(_db, "defaults.vm", "ssh_user"))
//	dns_server=str(SettingsService.resolve(_db, "defaults.vm", "dns_server"))
//	root_uid=int(SettingsService.resolve(_db, "defaults.vm", "root_uid"))
//	... etc.
//
// Network prefix and netmask are calculated from vm.network.subnet using
// ipaddress.IPv4Network (Go equivalent: net.ParseCIDR).
func (b *VMCreateBuilder) FromVM(ctx context.Context, vmEntity *model.VM) (*VMCreateResolved, error) {
	// Python raises errors if enriched relations are missing
	if vmEntity.Network == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMNetworkNotFound,
			Op:      "vm_from_vm",
			Message: fmt.Sprintf("Network not found for VM '%s' (ID: %s)", vmEntity.Name, vmEntity.NetworkID),
			Class:   errs.ClassValidation,
		}
	}
	if vmEntity.Image == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMImageNotFound,
			Op:      "vm_from_vm",
			Message: fmt.Sprintf("Image not found for VM '%s' (ID: %s)", vmEntity.Name, vmEntity.ImageID),
			Class:   errs.ClassValidation,
		}
	}
	if vmEntity.Kernel == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMKernelNotFound,
			Op:      "vm_from_vm",
			Message: fmt.Sprintf("Kernel not found for VM '%s' (ID: %s)", vmEntity.Name, vmEntity.KernelID),
			Class:   errs.ClassValidation,
		}
	}
	if vmEntity.Binary == nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMBinaryNotFound,
			Op:      "vm_from_vm",
			Message: fmt.Sprintf("Binary not found for VM '%s' (ID: %s)", vmEntity.Name, vmEntity.BinaryID),
			Class:   errs.ClassValidation,
		}
	}

	// Calculate network prefix and netmask from the VM's network subnet.
	// Python: ipv4_net = ipaddress.IPv4Network(vm.network.subnet, strict=False)
	_, ipv4Net, err := net.ParseCIDR(vmEntity.Network.Subnet)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_from_vm",
			Message: fmt.Sprintf("Invalid network subnet for VM '%s': %s", vmEntity.Name, vmEntity.Network.Subnet),
			Class:   errs.ClassValidation,
		}
	}
	networkPrefixLen, _ := ipv4Net.Mask.Size()
	networkNetmask := net.IP(ipv4Net.Mask).String()

	// extra_drives from volumes (Python: VolumeService.volumes_to_drives(vm.volumes))
	extraDrives := volume.VolumesToDrives(vmEntity.Volumes)

	// cloud-init mode (Python: CloudInitMode(vm.cloud_init_mode) if vm.cloud_init_mode else CloudInitMode.OFF)
	ciMode := vmEntity.CloudInitMode
	if ciMode == "" {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_from_vm",
			Message: fmt.Sprintf("Cloud-init mode is required for VM '%s'", vmEntity.Name),
			Class:   errs.ClassValidation,
		}
	}

	// boot_args (Python: vm.boot_args if vm.boot_args else SettingsService.resolve(...))
	var bootArgs string
	if vmEntity.BootArgs != nil && *vmEntity.BootArgs != "" {
		bootArgs = *vmEntity.BootArgs
	} else {
		bootArgs, _ = b.cfg.GetString(ctx, "defaults.vm", "boot_args")
	}

	// lsm_flags (Python: vm.lsm_flags if vm.lsm_flags else SettingsService.resolve(...))
	lsmFlags, _ := b.cfg.GetString(ctx, "defaults.vm", "lsm_flags")
	if vmEntity.LSMFlags != nil && *vmEntity.LSMFlags != "" {
		lsmFlags = *vmEntity.LSMFlags
	}

	// requested_guest_ip / requested_guest_mac (Python: vm.ipv4 / vm.mac — always set)
	requestedGuestIP := &vmEntity.IPv4
	requestedGuestMAC := &vmEntity.MAC

	// Resolve settings from config — Python uses SettingsService.resolve()
	var user string
	if vmEntity.SSHUser != nil && *vmEntity.SSHUser != "" {
		user = *vmEntity.SSHUser
	} else {
		user, _ = b.cfg.GetString(ctx, "defaults.vm", "ssh_user")
	}
	dnsServer, _ := b.cfg.GetString(ctx, "defaults.vm", "dns_server")
	rootUID, _ := b.cfg.GetInt(ctx, "defaults.vm", "root_uid")
	rootGID, _ := b.cfg.GetInt(ctx, "defaults.vm", "root_gid")
	userUID, _ := b.cfg.GetInt(ctx, "defaults.vm", "user_uid")
	userGID, _ := b.cfg.GetInt(ctx, "defaults.vm", "user_gid")
	guestMACPrefix, _ := b.cfg.GetString(ctx, "defaults.vm", "guest_mac_prefix")

	logLevel, _ := b.cfg.GetString(ctx, "defaults.firecracker", "log_level")
	logFilename, _ := b.cfg.GetString(ctx, "defaults.firecracker", "log_filename")
	serialOutputFilename, _ := b.cfg.GetString(ctx, "defaults.firecracker", "serial_output_filename")
	metricsFilename, _ := b.cfg.GetString(ctx, "defaults.firecracker", "metrics_filename")
	apiSocketFilename, _ := b.cfg.GetString(ctx, "defaults.firecracker", "api_socket_filename")
	pidFilename, _ := b.cfg.GetString(ctx, "defaults.firecracker", "pid_filename")
	configFilename, _ := b.cfg.GetString(ctx, "defaults.firecracker", "config_filename")
	consoleSocketFilename, _ := b.cfg.GetString(ctx, "defaults.firecracker", "console_socket_filename")
	consolePIDFilename, _ := b.cfg.GetString(ctx, "defaults.firecracker", "console_pid_filename")

	ciIsoName, _ := b.cfg.GetString(ctx, "defaults.cloudinit", "iso_name")
	nocloudPortStart, _ := b.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_port_range_start")
	nocloudPortEnd, _ := b.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_port_range_end")
	nocloudMaxRetries, _ := b.cfg.GetInt(ctx, "defaults.cloudinit", "nocloud_max_port_retries")

	resolved := &VMCreateResolved{
		Name:                vmEntity.Name,
		VMID:                vmEntity.ID,
		VMDir:               infra.GetVMDirByID(vmEntity.ID),
		VCPUCount:           vmEntity.VCPUCount,
		MemSizeMib:          vmEntity.MemSizeMiB,
		User:                user,
		DNSServer:           dnsServer,
		RootUID:             rootUID,
		RootGID:             rootGID,
		UserUID:             userUID,
		UserGID:             userGID,
		GuestMACPrefix:      guestMACPrefix,
		Network:             vmEntity.Network,
		Image:               vmEntity.Image,
		Kernel:              vmEntity.Kernel,
		Binary:              vmEntity.Binary,
		NetworkPrefixLen:    networkPrefixLen,
		NetworkNetmask:      networkNetmask,
		CloudInitMode:       model.CloudInitMode(ciMode),
		SkipCINetworkConfig: false,
		PCIEnabled:          vmEntity.PCIEnabled,
		NestedVirt:          vmEntity.NestedVirt,
		CPUConfig:           vmEntity.CPUConfig,
		EnableConsole:       vmEntity.EnableConsole,
		EnableLogging:       vmEntity.EnableLogging,
		EnableMetrics:       vmEntity.EnableMetrics,
		KeepCloudInitISO:    false,
		SkipCleanup:         false,
		SkipDeblob:          false,
		DiskSizeBytes:       int64(vmEntity.DiskSizeMiB) * disk.MebibyteBytes,
		DiskSizeMib:         vmEntity.DiskSizeMiB,
		LSMFlags:            lsmFlags,
		BootArgs:            &bootArgs,
		RequestedGuestIP:    requestedGuestIP,
		RequestedGuestMAC:   requestedGuestMAC,
		NocloudNetPort:      vmEntity.NocloudNetPort,
		CustomUserDataPath:  nil,
		CloudInitISOPath:    nil,
		SSHKeys:             []*model.SSHKeyItem{},
		Provisioner:         model.ProvisionerLoopMount,
		Volumes:             vmEntity.Volumes,
		ExtraDrives:         extraDrives,
		LogLevel:                logLevel,
		LogFilename:             logFilename,
		SerialOutputFilename:    serialOutputFilename,
		MetricsFilename:         metricsFilename,
		APISocketFilename:       apiSocketFilename,
		PIDFilename:             pidFilename,
		ConfigFilename:          configFilename,
		ConsoleSocketFilename:   consoleSocketFilename,
		ConsolePIDFilename:      consolePIDFilename,
		CloudInitISOName:        ciIsoName,
		NocloudPortRangeStart:   nocloudPortStart,
		NocloudPortRangeEnd:     nocloudPortEnd,
		NocloudMaxPortRetries:   nocloudMaxRetries,
	}

	return resolved, nil
}
