package inputs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/errs"

	"github.com/jmoiron/sqlx"
)

// VMImportInput is the raw import parameters from CLI.
// Matches Python's VMImportInput dataclass:
//
//	@dataclass
//	class VMImportInput:
//	    config_path: Path
//	    name_override: str | None = None
type VMImportInput struct {
	ConfigPath   string  `json:"config_path"`
	NameOverride *string `json:"name_override,omitempty"`
}

// VMImportRequest matches Python's VMImportRequest.
//
// Resolve VMImportInput to ResolvedVMCreateInput.
// Python delegates to VMCreateRequest for full resolution.
type VMImportRequest struct {
	cfg   *config.Service
	db    *sqlx.DB
	input VMImportInput
}

// NewVMImportRequest creates a new VMImportRequest.
func NewVMImportRequest(inputs VMImportInput, cfg *config.Service, db *sqlx.DB) *VMImportRequest {
	return &VMImportRequest{
		cfg:   cfg,
		db:    db,
		input: inputs,
	}
}

// Resolve resolves import config to fully resolved VM creation parameters.
// Matches Python's VMImportRequest.resolve():
//
//  1. Read VMExportConfig from JSON file
//  2. Resolve semantic references to DB records
//  3. Build VMCreateInput with resolved values
//  4. Delegate to VMCreateRequest for full resolution
func (r *VMImportRequest) Resolve(ctx context.Context) (*ResolvedVMCreateInput, error) {
	// 1. Read export config from JSON file
	exportConfig, err := FromVMExportConfigJSONFile(r.input.ConfigPath)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Op:      "vm_import",
			Message: fmt.Sprintf("Failed to read config: %s", err.Error()),
			Class:   errs.ClassValidation,
		}
	}

	// 2. Create repos from DB (matching Python's per-request repo creation)
	imageRepo := image.NewRepository(r.db)
	kernelRepo := kernel.NewRepository(r.db)
	binaryRepo := binary.NewRepository(r.db)
	networkRepo := network.NewRepository(r.db)
	vmRepo := vm.NewRepository(r.db)
	keyRepo := key.NewRepository(r.db)
	volumeRepo := volume.NewRepository(r.db)
	leaseRepo := network.NewLeaseRepository(r.db)

	// 3. Resolve all assets from semantic references
	imageSlug, err := r.resolveImage(ctx, exportConfig.Image, imageRepo)
	if err != nil {
		return nil, err
	}

	kernelID, err := r.resolveKernel(ctx, exportConfig.Kernel, kernelRepo)
	if err != nil {
		return nil, err
	}

	binaryID, err := r.resolveBinary(ctx, exportConfig.Binary, binaryRepo)
	if err != nil {
		return nil, err
	}

	networkName, err := r.resolveNetwork(ctx, exportConfig.Network, networkRepo)
	if err != nil {
		return nil, err
	}

	// 4. Build name
	vmName := exportConfig.Name
	if r.input.NameOverride != nil && *r.input.NameOverride != "" {
		vmName = *r.input.NameOverride
	}

	// 5. Parse cpu_config from export JSON string (matching Python)
	var cpuConfig map[string]any
	if exportConfig.Firecracker.CPUConfig != "" {
		// Python catches JSONDecodeError and logs a warning (non-fatal).
		// Python: if cpu_config is not None: try json.loads(cpu_config) except JSONDecodeError: logger.warning(...)
		if err := json.Unmarshal([]byte(exportConfig.Firecracker.CPUConfig), &cpuConfig); err != nil {
			slog.Warn("Failed to parse cpu_config from import file",
				"cpu_config", exportConfig.Firecracker.CPUConfig,
				"error", err,
			)
		}
	}

	// 6. Build VMCreateInput with resolved values (matching Python exactly)
	createInput := VMCreateInput{
		Name:                vmName,
		SSHKeys:             []string{},
		Image:               imageSlug,
		KernelID:            kernelID,
		BinaryID:            binaryID,
		NetworkName:         networkName,
		PCIEnabled:          exportConfig.Firecracker.PCIEnabled,
		EnableConsole:       exportConfig.Boot.EnableConsole,
		BootArgs:            exportConfig.Boot.Args,
		NestedVirt:          exportConfig.Firecracker.NestedVirt,
		CPUConfig:           cpuConfig,
		Atomic:              false,
		SkipCINetworkConfig: false,
		KeepCloudInitISO:    false,
		SkipCleanup:         false,
		SkipDeblob:          false,
		Count:               nil,
		Volumes:             nil,
	}

	// mem_size_mib: Python does str(export_config.compute.mem)
	// Python: str(None) → "None", str(2048) → "2048"
	if exportConfig.Compute.Mem > 0 {
		createInput.MemSizeMib = strconv.Itoa(exportConfig.Compute.Mem)
	} else {
		createInput.MemSizeMib = "None"
	}

	// vcpu_count: set only when specified
	if exportConfig.Compute.VCPUs > 0 {
		v := exportConfig.Compute.VCPUs
		createInput.VCPUCount = &v
	}

	// nocloud_net_port: set only when specified
	if exportConfig.CloudInit.NocloudNetPort > 0 {
		p := exportConfig.CloudInit.NocloudNetPort
		createInput.NocloudNetPort = &p
	}

	// disk_size: Python passes export_config.image.disk_size directly (could be None)
	if exportConfig.Image.DiskSize != "" {
		createInput.DiskSize = exportConfig.Image.DiskSize
	}

	// lsm_flags: Python passes export_config.firecracker.lsm_flags directly (could be None)
	if exportConfig.Firecracker.LsmFlags != "" {
		createInput.LSMFlags = exportConfig.Firecracker.LsmFlags
	}

	// requested_guest_ip / requested_guest_mac: set only when specified
	if exportConfig.Network.IP != "" {
		ip := exportConfig.Network.IP
		createInput.RequestedGuestIP = &ip
	}
	if exportConfig.Network.MAC != "" {
		mac := exportConfig.Network.MAC
		createInput.RequestedGuestMAC = &mac
	}

	// cloud_init_mode / user: set only when specified
	if exportConfig.CloudInit.Mode != "" {
		mode := exportConfig.CloudInit.Mode
		createInput.CloudInitMode = &mode
	}
	if exportConfig.CloudInit.User != "" {
		user := exportConfig.CloudInit.User
		createInput.User = &user
	}

	// 7. Generate vm_id and vm_dir (matching Python: HashGenerator.vm + CacheUtils.get_vm_dir)
	now := time.Now()
	ts := now.Format(time.RFC3339)
	vmID := crypto.VMID(vmName, ts)
	vmDir := infra.GetVMDirByID(vmID)

	// 8. Delegate to VMCreateRequest for full resolution (matching Python's VMCreateRequest.resolve())
	request := NewVMCreateRequest(
		vmID, vmDir, createInput,
		r.cfg,
		vmRepo,
		networkRepo,
		imageRepo,
		kernelRepo,
		binaryRepo,
		keyRepo,
		volumeRepo,
		leaseRepo,
	)
	return request.Resolve(ctx)
}

func (r *VMImportRequest) resolveImage(
	ctx context.Context,
	imgConfig VMExportImageConfig,
	imageRepo image.Repository,
) (*string, error) {
	if imgConfig.Type == "" {
		return nil, nil
	}
	resolver := image.NewResolver(imageRepo)
	img, err := resolver.ByType(ctx, imgConfig.Type)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeImageNotFound,
			Op:   "vm_import",
			Message: fmt.Sprintf(
				"Image '%s' not found. Fetch it first: mvm image pull %s",
				imgConfig.Type,
				imgConfig.Type,
			),
			Class: errs.ClassValidation,
		}
	}
	t := img.Type
	return &t, nil
}

func (r *VMImportRequest) resolveKernel(
	ctx context.Context,
	knlConfig VMExportKernelConfig,
	kernelRepo kernel.Repository,
) (*string, error) {
	if knlConfig.Version == "" || knlConfig.Type == "" {
		return nil, nil
	}
	resolver := kernel.NewResolver(kernelRepo, nil)
	krnl, err := resolver.ByVersionType(ctx, knlConfig.Version, knlConfig.Type)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeKernelNotFound,
			Op:   "vm_import",
			// Python uses !r (repr) which adds quotes: version='6.1.0', type='vmlinux'
			Message: fmt.Sprintf(
				"Kernel version=%q, type=%q not found. Fetch it first: mvm kernel pull --type %s",
				knlConfig.Version,
				knlConfig.Type,
				knlConfig.Type,
			),
			Class: errs.ClassValidation,
		}
	}
	return &krnl.ID, nil
}

func (r *VMImportRequest) resolveBinary(
	ctx context.Context,
	binConfig VMExportBinaryConfig,
	binaryRepo binary.Repository,
) (*string, error) {
	if binConfig.Version == "" {
		return nil, nil
	}
	resolver := binary.NewResolver(binaryRepo)
	bin, err := resolver.ByNameVersion(ctx, binConfig.Name, binConfig.Version)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeBinaryNotFound,
			Op:   "vm_import",
			Message: fmt.Sprintf(
				"Binary '%s' version='%s' not found. Fetch it first: mvm bin pull %s",
				binConfig.Name,
				binConfig.Version,
				binConfig.Version,
			),
			Class: errs.ClassValidation,
		}
	}
	return &bin.ID, nil
}

func (r *VMImportRequest) resolveNetwork(
	ctx context.Context,
	netConfig VMExportNetworkConfig,
	networkRepo network.Repository,
) (*string, error) {
	if netConfig.Name == "" {
		return nil, nil
	}
	resolver := network.NewResolver(networkRepo, nil)
	net, err := resolver.ByName(ctx, netConfig.Name)
	if err != nil {
		return nil, &errs.DomainError{
			Code: errs.CodeNetworkNotFound,
			Op:   "vm_import",
			Message: fmt.Sprintf(
				"Network '%s' not found. Create it first: mvm network create %s",
				netConfig.Name,
				netConfig.Name,
			),
			Class: errs.ClassValidation,
		}
	}
	return &net.Name, nil
}

// generateVMID generates a VM ID from name and timestamp.
// Matches Python's HashGenerator.vm():
//
//	data = f"{name}:{created_at}"
//	return hashlib.sha256(data.encode()).hexdigest()[:32]
