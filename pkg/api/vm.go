// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/vm_operations.py exactly.
package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/cloudinit"
	"mvmctl/internal/core/console"
	"mvmctl/internal/core/host"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/ssh"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/enricher"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/provisioner"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
	nocloudnet "mvmctl/internal/service/nocloudnet"
	consoleapi "mvmctl/internal/service/console"
	"mvmctl/pkg/api/inputs"
)

// VMOperation provides the public orchestration API for all VM lifecycle operations.
// Matches Python's VMOperation exactly. All methods return OperationResult/BatchResult.
type VMOperation struct {
	db         *sql.DB
	cacheDir   string
	vmRepo     vm.Repository
	netRepo    network.Repository
	imageRepo  image.Repository
	kernelRepo kernel.Repository
	binaryRepo binary.Repository
	keyRepo    key.Repository
	volRepo    volume.Repository
	enr        *enricher.Enricher
}

// NewVMOperation creates a VMOperation with all dependencies.
func NewVMOperation(
	cacheDir string,
	db *sql.DB,
	vmRepo vm.Repository,
	netRepo network.Repository,
	imageRepo image.Repository,
	kernelRepo kernel.Repository,
	binaryRepo binary.Repository,
	keyRepo key.Repository,
	volRepo volume.Repository,
	enr *enricher.Enricher,
) *VMOperation {
	return &VMOperation{
		db:         db,
		cacheDir:   cacheDir,
		vmRepo:     vmRepo,
		netRepo:    netRepo,
		imageRepo:  imageRepo,
		kernelRepo: kernelRepo,
		binaryRepo: binaryRepo,
		keyRepo:    keyRepo,
		volRepo:    volRepo,
		enr:        enr,
	}
}

// ── Create ──

// Create creates one or more VMs.
// Matches Python's VMOperation.create() exactly.
func (o *VMOperation) Create(ctx context.Context, input *inputs.VMCreateInput, onProgress func(errs.ProgressEvent)) *errs.OperationResult {
	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "create VMs"); err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodePrivilegeRequired),
			Message:   fmt.Sprintf("Privilege check failed: %v", err),
			Exception: err,
		}
	}

	count := 1
	if input.Count != nil && *input.Count > 1 {
		count = *input.Count
	}

	if count == 1 {
		return o.createSingle(ctx, input, onProgress)
	}

	return o.createBatch(ctx, input, count, onProgress)
}

func (o *VMOperation) createSingle(ctx context.Context, input *inputs.VMCreateInput, onProgress func(errs.ProgressEvent)) *errs.OperationResult {
	createdAt := time.Now().UTC()
	vmID := infra.HashGenerator{}.VM(input.Name, createdAt.Format(time.RFC3339))
	vmDir := filepath.Join(o.cacheDir, "vms", vmID)

	resolved, err := o.buildResolvedInput(ctx, input, vmID, vmDir)
	if err != nil {
		status := "error"
		var de *errs.DomainError
		if !errors.As(err, &de) {
			status = "failure"
		}
		return &errs.OperationResult{
			Status: status, Code: string(errs.CodeVMCreateFailed),
			Message:   fmt.Sprintf("Failed to resolve input: %v", err),
			Exception: err,
		}
	}

	// Set up signal-based cleanup (matches Python's SigtermContext(lambda: ctx.cleanup()))
	createCtx, cancelCreate := context.WithCancel(ctx)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	var vmCleanup func()

	go func() {
		select {
		case <-sigCh:
			slog.Warn("Received termination signal during VM creation, cleaning up...", "vm", input.Name)
			// Call cleanup directly on signal (matches Python's SigtermContext behavior).
			// This runs regardless of SkipCleanup, matching Python.
			if vmCleanup != nil {
				vmCleanup()
			}
			cancelCreate()
		case <-createCtx.Done():
		}
	}()
	signalCleanup := func() {
		signal.Stop(sigCh)
		close(sigCh)
		cancelCreate()
	}

	vmInstance, execErr := o.executeCreate(createCtx, resolved, onProgress, &vmCleanup)
	signalCleanup()
	if execErr != nil {
		// Python's SigtermContext already called cleanup on signal regardless of skip_cleanup.
		// For non-signal errors, skip_cleanup is respected.
		if resolved.SkipCleanup {
			slog.Warn("VM creation failed but --skip-cleanup is active", "dir", vmDir)
		}
		status := "error"
		var de *errs.DomainError
		if !errors.As(execErr, &de) {
			status = "failure"
		}
		return &errs.OperationResult{
			Status: status, Code: string(errs.CodeVMCreateFailed),
			Message:   execErr.Error(),
			Exception: execErr,
		}
	}

	// Handle volumes
	if len(resolved.Volumes) > 0 {
		volSvc := volume.NewService(o.volRepo)
		volSvc.SetVolumesState(ctx, resolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
		vmInstance.VolumeIDs = make([]string, len(resolved.Volumes))
		for i, v := range resolved.Volumes {
			vmInstance.VolumeIDs[i] = v.ID
		}
		o.vmRepo.Upsert(ctx, vmInstance)
	}

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("vm.create", nil, fmt.Sprintf("name=%s", input.Name))

	return &errs.OperationResult{
		Status: "success", Code: "vm.created",
		Item:    []*model.VM{vmInstance},
		Message: fmt.Sprintf("VM '%s' created", input.Name),
	}
}

func (o *VMOperation) createBatch(ctx context.Context, input *inputs.VMCreateInput, count int, onProgress func(errs.ProgressEvent)) *errs.OperationResult {
	names := o.generateBatchNames(input.Name, count)

	// Pre-allocate: check name collisions (single query, matching Python)
	existing, err := o.vmRepo.GetByNames(ctx, names)
	if err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeDatabaseError),
			Message:   fmt.Sprintf("Failed to check name collisions: %v", err),
			Exception: err,
		}
	}
	if len(existing) > 0 {
		sortedNames := make([]string, 0, len(existing))
		for name := range existing {
			sortedNames = append(sortedNames, name)
		}
		sort.Strings(sortedNames)
		return &errs.OperationResult{
			Status: "error", Code: "vm.name_collision",
			Message: fmt.Sprintf("VM name(s) already exist: %s", strings.Join(sortedNames, ", ")),
		}
	}

	createdVMs := make([]*model.VM, 0)
	errors := make([]string, 0)

	for idx, name := range names {
		createdAt := time.Now().UTC()
		vmID := infra.HashGenerator{}.VM(name, createdAt.Format(time.RFC3339))
		vmDir := filepath.Join(o.cacheDir, "vms", vmID)

		resolved, err := o.buildResolvedInput(ctx, input, vmID, vmDir)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, err))
			if input.Atomic && len(createdVMs) > 0 {
				// Rollback
				for _, vm := range createdVMs {
					_ = o.Remove(ctx, &inputs.VMInput{Identifiers: []string{vm.Name}, Force: boolPtr(true)})
				}
				return &errs.OperationResult{
					Status: "error", Code: "vm.atomic_failed",
					Message: fmt.Sprintf("Atomic creation failed at '%s': %v. All %d previously created VMs have been removed.",
						name, err, len(createdVMs)),
				}
			}
			continue
		}

		batchProgress := func(event errs.ProgressEvent) {
			if onProgress != nil {
				onProgress(errs.ProgressEvent{
					Phase:   event.Phase,
					Status:  event.Status,
					Message: fmt.Sprintf("[%d/%d] %s: %s", idx+1, count, name, event.Message),
				})
			}
		}

		vmInstance, execErr := o.executeCreateWithOpts(ctx, resolved, batchProgress, nil, true)
		if execErr != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, execErr))
			if input.Atomic && len(createdVMs) > 0 {
				for _, vm := range createdVMs {
					_ = o.Remove(ctx, &inputs.VMInput{Identifiers: []string{vm.Name}, Force: boolPtr(true)})
				}
				return &errs.OperationResult{
					Status: "error", Code: "vm.atomic_failed",
					Message: fmt.Sprintf("Atomic creation failed at '%s': %v. All %d previously created VMs have been removed.",
						name, execErr, len(createdVMs)),
				}
			}
			continue
		}

		// Handle volumes for batch VM (matches Python's volume handling after _execute_create)
		if resolved.Volumes != nil && len(resolved.Volumes) > 0 {
			volSvc := volume.NewService(o.volRepo)
			volSvc.SetVolumesState(ctx, resolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
			vmInstance.VolumeIDs = make([]string, len(resolved.Volumes))
			for i, v := range resolved.Volumes {
				vmInstance.VolumeIDs[i] = v.ID
			}
			o.vmRepo.Upsert(ctx, vmInstance)
		}
		createdVMs = append(createdVMs, vmInstance)
	}

	if len(errors) > 0 && len(createdVMs) == 0 {
		return &errs.OperationResult{
			Status: "error", Code: "vm.create_failure",
			Message: strings.Join(errors, "; "),
		}
	}

	message := fmt.Sprintf("Created %d VM(s): ", len(createdVMs))
	for i, vm := range createdVMs {
		if i > 0 {
			message += ", "
		}
		message += vm.Name
	}
	if len(errors) > 0 {
		message += fmt.Sprintf("\nFailed: %s", strings.Join(errors, "; "))
	}

	status := "success"
	if len(errors) > 0 {
		status = "warning"
	}

	return &errs.OperationResult{
		Status: status, Code: "vm.created_batch",
		Item:    createdVMs,
		Message: message,
	}
}

func (o *VMOperation) executeCreate(ctx context.Context, resolved *resolvedVMCreateInput, onProgress func(errs.ProgressEvent), cleanupFn *func()) (*model.VM, error) {
	return o.executeCreateWithOpts(ctx, resolved, onProgress, cleanupFn, false)
}

func (o *VMOperation) executeCreateWithOpts(ctx context.Context, resolved *resolvedVMCreateInput, onProgress func(errs.ProgressEvent), cleanupFn *func(), skipLimitCheck bool) (*model.VM, error) {
	vmRepo := o.vmRepo

	// Check VM limit (Python: SettingsService.resolve(Database(), "settings.vm", "max_vms"))
	if !skipLimitCheck {
		maxVMs := 10
		if o.db != nil {
			row := o.db.QueryRowContext(ctx, "SELECT value FROM user_settings WHERE category = 'settings.vm' AND key = 'max_vms'")
			var val string
			if err := row.Scan(&val); err == nil {
				if n, err := strconv.Atoi(val); err == nil && n > 0 {
					maxVMs = n
				}
			}
		}
		count, err := vmRepo.Count(ctx)
		if err != nil {
			return nil, fmt.Errorf("count VMs: %w", err)
		}
		if count >= maxVMs {
			return nil, fmt.Errorf("VM limit reached (%d). Remove existing VMs before creating new ones.", maxVMs)
		}
	}

	// Create context
	ctxCreate := &vmCreateContext{
		name:             resolved.Name,
		vmID:             resolved.VMID,
		vmDir:            resolved.VMDir,
		onProgress:       onProgress,
		resolved:         resolved,
		resourcesCreated: make(map[string]bool),
		cacheDir:         o.cacheDir,
		db:               o.db,
	}

	// Set the cleanup function so signal handlers can call it
	// (matches Python's SigtermContext pattern where cleanup is triggered on signal)
	if cleanupFn != nil {
		*cleanupFn = ctxCreate.cleanup
	}

	var vmInstance *model.VM
	var execErr error

	// Execute
	execErr = ctxCreate.execute(ctx)
	if execErr == nil {
		vmInstance = ctxCreate.toModel()
		// Python: if vm_instance is None: raise VMCreateError("Failed to create VM instance model")
		if vmInstance == nil {
			if ctxCreate.spawner == nil {
				execErr = fmt.Errorf("Firecracker spawner is not set in context")
			} else if ctxCreate.spawner.PID() == nil {
				execErr = fmt.Errorf("Failed to spawn Firecracker process")
			} else {
				execErr = fmt.Errorf("Failed to create VM instance model")
			}
		} else if err := vmRepo.Upsert(ctx, vmInstance); err != nil {
			execErr = fmt.Errorf("upsert VM: %w", err)
		}
	}

	if execErr != nil {
		if !resolved.SkipCleanup {
			ctxCreate.cleanup()
		}
		return nil, execErr
	}

	return vmInstance, nil
}

// ── Remove ──

// Remove removes one or more VMs.
// Matches Python's VMOperation.remove() exactly.
// Uses the proper VMRequest pipeline (validation + resolution + enrichment)
// instead of inline resolution, matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (o *VMOperation) Remove(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "Remove VM"); err != nil {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(errs.CodePrivilegeRequired),
					Message: fmt.Sprintf("Privilege check failed: %v", err), Exception: err},
			},
		}
	}

	// Use VMRequest pipeline (matches Python's VMRequest(inputs=inputs, db=db).resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		o.db,
		o.vmRepo,
		o.enr,
	)
	resolved, err := vmRequest.Resolve(ctx)
	if err != nil {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(errs.CodeVMNotFound),
					Message: fmt.Sprintf("No VMs found matching the given identifiers: %v", err)},
			},
		}
	}

	if len(resolved.VMs) == 0 {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(errs.CodeVMNotFound),
					Message: "No VMs found matching the given identifiers"},
			},
		}
	}

	results := make([]errs.OperationResult, 0)

	// Report identifiers that couldn't be resolved (matches Python's logger.warning + error result)
	unresolvedCount := len(input.Identifiers) - len(resolved.VMs)
	if unresolvedCount > 0 {
		slog.Warn("VM rm: identifier(s) could not be resolved", "unresolved", unresolvedCount, "total", len(input.Identifiers))
		results = append(results, errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMNotFound),
			Message: fmt.Sprintf("%d VM identifier(s) not found", unresolvedCount),
		})
	}

	repo := o.vmRepo
	volSvc := volume.NewService(o.volRepo)

	for _, v := range resolved.VMs {
		vmLocal := v
		vmDir := filepath.Join(o.cacheDir, "vms", vmLocal.ID)

		// Stop the VM
		controller, ctrlErr := vm.NewController(vmLocal, repo)
		if ctrlErr == nil {
			controller.Stop(ctx, resolved.Force)
		}

		// Defense-in-depth: force-kill
		if vmLocal.PID > 0 && isProcessRunningGo(vmLocal.PID) {
			proc, err := os.FindProcess(vmLocal.PID)
			if err == nil {
				_ = proc.Kill()
			}
		}

		// Perform removal cleanup
		o.performRemovalCleanup(vmLocal)

		// Detach volumes
		if len(vmLocal.VolumeIDs) > 0 {
			var vols []*model.VolumeItem
			for _, vid := range vmLocal.VolumeIDs {
				v, _ := o.volRepo.Get(ctx, vid)
				if v != nil {
					vols = append(vols, v)
				}
			}
			if len(vols) > 0 {
				_ = volSvc.SetVolumesState(ctx, vols, model.VolumeStatusAvailable, nil)
			}
		}

		// Delete from DB
		_ = repo.Delete(ctx, vmLocal.ID)
		if vmDir != "" {
			os.RemoveAll(vmDir)
		}

		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("vm.remove", map[string]interface{}{"name": vmLocal.Name}, "")

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.removed",
			Item: vmLocal, Message: fmt.Sprintf("VM '%s' removed", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

func (o *VMOperation) performRemovalCleanup(vm *model.VM) {
	ctx := context.Background()

	// Console relay cleanup (matches Python's _cleanup_console)
	if vm.RelayPID != nil && vm.ID != "" {
		relay := consoleapi.NewRelayManager(vm.ID, filepath.Join(o.cacheDir, "vms", vm.ID), vm.Name,
			"console.pid", "console.sock", "firecracker.console.log")
		relay.Stop(true)
	}

	// TAP device cleanup (matches Python's _cleanup_network)
	if vm.TapDevice != "" && vm.NetworkID != "" {
		netSvc := network.NewService(network.NewRepository(nil), nil)
		_ = netSvc.RemoveTap(ctx, vm.TapDevice, "", vm.NetworkID)
	}

	// IP lease cleanup (matches Python's _cleanup_ip)
	if vm.ID != "" {
		leaseRepo := network.NewLeaseRepository(o.db)
		_ = leaseRepo.ReleaseByVM(ctx, vm.ID)
	}

	// SSH known hosts cleanup (matches Python's ssh-keygen -R {ipv4})
	if vm.IPv4 != "" {
		_ = system.RunCmdCompat(ctx, []string{"ssh-keygen", "-R", vm.IPv4}, system.RunCmdOptions{Check: false, Capture: false})
	}
}

// ── Prune ──

// Prune prunes VMs.
// Matches Python's VMOperation.prune() exactly.
func (o *VMOperation) Prune(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "prune VMs"); err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodePrivilegeRequired),
			Message: fmt.Sprintf("Privilege check failed: %v", err), Exception: err,
		}
	}

	allVMs, err := o.vmRepo.ListAll(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeDatabaseError),
			Message: fmt.Sprintf("Failed to list VMs: %v", err), Exception: err,
		}
	}

	removed := make([]string, 0)
	for _, vm := range allVMs {
		if vm.Status == model.StatusRunning || vm.Status == model.StatusStarting {
			if !includeAll {
				continue
			}
		}

		if !dryRun {
			result := o.Remove(ctx, &inputs.VMInput{Identifiers: []string{vm.Name}, Force: boolPtr(true)})
			if result.HasErrors() {
				slog.Warn("Failed to remove VM", "name", vm.Name, "error", joinStringsPtrs(result))
				continue
			}
		}
		removed = append(removed, vm.Name)
	}

	return &errs.OperationResult{
		Status: "success", Code: "cache.pruned",
		Message: fmt.Sprintf("Pruned %d VM(s)", len(removed)),
		Item:    removed,
	}
}

// ── List / ToJSON ──

// List returns all VMs, optionally filtered by status.
// Matches Python's VMOperation.list_all() exactly.
func (o *VMOperation) List(ctx context.Context, statusFilter interface{}) []*model.VM {
	var vms []*model.VM
	var err error

	if statusFilter != nil {
		switch s := statusFilter.(type) {
		case string:
			vms, err = o.vmRepo.ListByStatus(ctx, s)
		case []string:
			vms, err = o.vmRepo.ListByStatus(ctx, s...)
		default:
			vms, err = o.vmRepo.ListAll(ctx)
		}
	} else {
		vms, err = o.vmRepo.ListAll(ctx)
	}

	if err != nil || len(vms) == 0 {
		return vms
	}

	if o.enr != nil {
		_ = o.enr.EnrichVM(ctx, vms)
	}

	return vms
}

// ToJSON converts VMs to JSON-serializable dicts.
// Matches Python's VMOperation.to_json() exactly.
// Python always includes ALL fields in every entry (with None/null if not set).
func (o *VMOperation) ToJSON(vms []*model.VM) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(vms))
	for _, vm := range vms {
		// Always include all fields — Python always sets every key in the dict,
		// using None for missing values. For Go, use interface{}(nil) for nil
		// pointer fields to produce JSON `null`, matching Python's `None`.
		var processStartTime interface{} = nil
		if vm.ProcessStartTime != nil {
			processStartTime = *vm.ProcessStartTime
		}
		var nocloudNetPort interface{} = nil
		if vm.NocloudNetPort != nil {
			nocloudNetPort = *vm.NocloudNetPort
		}
		var nocloudNetPID interface{} = nil
		if vm.NocloudNetPID != nil {
			nocloudNetPID = *vm.NocloudNetPID
		}
		var relayPID interface{} = nil
		if vm.RelayPID != nil {
			relayPID = *vm.RelayPID
		}
		var relaySocketPath interface{} = nil
		if vm.RelaySocketPath != nil {
			relaySocketPath = *vm.RelaySocketPath
		}
		var logPath interface{} = nil
		if vm.LogPath != nil {
			logPath = *vm.LogPath
		}
		var serialOutputPath interface{} = nil
		if vm.SerialOutputPath != nil {
			serialOutputPath = *vm.SerialOutputPath
		}
		var lsmFlags interface{} = nil
		if vm.LSMFlags != nil {
			lsmFlags = *vm.LSMFlags
		}
		var bootArgs interface{} = nil
		if vm.BootArgs != nil {
			bootArgs = *vm.BootArgs
		}

		// Network enrichment fields — always included, null if not enriched
		var networkName interface{} = nil
		var networkSubnet interface{} = nil
		var networkBridge interface{} = nil
		var networkGateway interface{} = nil
		if vm.Network != nil {
			networkName = vm.Network.Name
			networkSubnet = vm.Network.Subnet
			networkBridge = vm.Network.Bridge
			networkGateway = vm.Network.IPv4Gateway
		}

		entry := map[string]interface{}{
			"id":                 vm.ID,
			"name":               vm.Name,
			"status":             vm.Status,
			"pid":                vm.PID,
			"exit_code":          vm.ExitCode,
			"ipv4":               vm.IPv4,
			"mac":                vm.MAC,
			"network_id":         vm.NetworkID,
			"network_name":       networkName,
			"network_subnet":     networkSubnet,
			"network_bridge":     networkBridge,
			"network_gateway":    networkGateway,
			"tap_device":         vm.TapDevice,
			"image_id":           vm.ImageID,
			"kernel_id":          vm.KernelID,
			"binary_id":          vm.BinaryID,
			"vcpu_count":         vm.VCPUCount,
			"mem_size_mib":       vm.MemSizeMiB,
			"disk_size_mib":      vm.DiskSizeMiB,
			"api_socket_path":    vm.APISocketPath,
			"config_path":        vm.ConfigPath,
			"cloud_init_mode":    vm.CloudInitMode,
			"rootfs_path":        vm.RootfsPath,
			"rootfs_suffix":      vm.RootfsSuffix,
			"pci_enabled":        vm.PCIEnabled,
			"enable_logging":     vm.EnableLogging,
			"enable_metrics":     vm.EnableMetrics,
			"enable_console":     vm.EnableConsole,
			"created_at":         vm.CreatedAt,
			"updated_at":         vm.UpdatedAt,
			"relay_socket_path":  relaySocketPath,
			"process_start_time": processStartTime,
			"nocloud_net_port":   nocloudNetPort,
			"nocloud_net_pid":    nocloudNetPID,
			"relay_pid":          relayPID,
			"log_path":           logPath,
			"serial_output_path": serialOutputPath,
			"lsm_flags":          lsmFlags,
			"boot_args":          bootArgs,
			"ssh_keys":           vm.SSHKeys,
			"ssh_user":           vm.SSHUser,
		}

		result = append(result, entry)
	}
	return result
}

// ── Get / Inspect ──

// Get returns a single VM by identifier.
// Matches Python's VMOperation.get() exactly.
func (o *VMOperation) Get(ctx context.Context, input *inputs.VMInput) (*model.VM, error) {
	if len(input.Identifiers) != 1 {
		return nil, fmt.Errorf("Expected exactly one VM identifier")
	}
	// Use the full resolution pipeline (name, IP, MAC, ID prefix) matching Python's VMResolver
	vm, err := o.resolveSingleVM(ctx, input.Identifiers[0])
	if err != nil {
		return nil, err
	}
	// Enrich VM with relations (matches Python's VMResolver._enrich)
	if o.enr != nil {
		_ = o.enr.EnrichVM(ctx, []*model.VM{vm})
	}
	return vm, nil
}

// Inspect returns detailed VM info with enriched data.
// Matches Python's VMOperation.inspect() exactly.
func (o *VMOperation) Inspect(ctx context.Context, input *inputs.VMInput) (map[string]interface{}, error) {
	vm, err := o.Get(ctx, input)
	if err != nil {
		return nil, err
	}

	// Resolve asset names (matches Python's Repository(db).get(vm.image_id) etc.)
	var imageName *string
	if vm.ImageID != "" {
		img, err := o.imageRepo.Get(ctx, vm.ImageID)
		if err == nil && img != nil {
			imageName = &img.Name
		}
	}
	var kernelVersion *string
	if vm.KernelID != "" {
		krn, err := o.kernelRepo.Get(ctx, vm.KernelID)
		if err == nil && krn != nil {
			kernelVersion = &krn.Version
		}
	}
	var networkName *string
	if vm.NetworkID != "" {
		net, err := o.netRepo.Get(ctx, vm.NetworkID)
		if err == nil && net != nil {
			networkName = &net.Name
		}
	}
	var binaryName *string
	if vm.BinaryID != "" {
		bin, err := o.binaryRepo.Get(ctx, vm.BinaryID)
		if err == nil && bin != nil {
			binaryName = &bin.Name
		}
	}

	// Console relay status — check actual relay process (matches Python's ConsoleRelayManager.is_running())
	relayRunning := false
	relayPID := vm.RelayPID
	relaySocketPath := vm.RelaySocketPath
	if vm.ID != "" && vm.RelayPID != nil {
		relay := consoleapi.NewRelayManager(vm.ID, filepath.Join(o.cacheDir, "vms", vm.ID), vm.Name,
			"console.pid", "console.sock", "firecracker.console.log")
		relayRunning = relay.IsRunning()
	}

	vmDir := filepath.Join(o.cacheDir, "vms", vm.ID)
	rootfsPath := filepath.Join(vmDir, "rootfs."+vm.RootfsSuffix)
	if vm.RootfsSuffix == "" {
		rootfsPath = filepath.Join(vmDir, "rootfs.ext4")
	}

	var configPath *string
	if vm.ConfigPath != "" {
		p := filepath.Join(vmDir, vm.ConfigPath)
		configPath = &p
	}

	var logPath *string
	if vm.LogPath != nil {
		p := filepath.Join(vmDir, *vm.LogPath)
		logPath = &p
	}

	var serialPath *string
	if vm.SerialOutputPath != nil {
		p := filepath.Join(vmDir, *vm.SerialOutputPath)
		serialPath = &p
	}

	// Resolve volumes with enrichment (matches Python's vm.volumes)
	volumes := make([]map[string]interface{}, 0)
	if len(vm.VolumeIDs) > 0 {
		vols, err := o.volRepo.FindByIDs(ctx, vm.VolumeIDs)
		if err == nil {
			for _, v := range vols {
				volumes = append(volumes, map[string]interface{}{
					"id":     v.ID,
					"name":   v.Name,
					"size":   v.SizeBytes,
					"format": v.Format,
					"status": v.Status,
				})
			}
		}
	}

	return map[string]interface{}{
		"vm": map[string]interface{}{
			"name":             vm.Name,
			"id":               vm.ID,
			"status":           vm.Status,
			"pid":              vm.PID,
			"exit_code":        vm.ExitCode,
			"ssh_keys":         vm.SSHKeys,
			"ssh_user":         vm.SSHUser,
			"cloud_init_mode":  vm.CloudInitMode,
			"nocloud_net_port": vm.NocloudNetPort,
			"nocloud_net_pid":  vm.NocloudNetPID,
			"pci_enabled":      vm.PCIEnabled,
			"enable_console":   vm.EnableConsole,
			"enable_logging":   vm.EnableLogging,
			"enable_metrics":   vm.EnableMetrics,
			"created_at":       vm.CreatedAt,
			"updated_at":       vm.UpdatedAt,
		},
		"resources": map[string]interface{}{
			"vcpus": vm.VCPUCount,
			"mem":   vm.MemSizeMiB,
			"disk":  vm.DiskSizeMiB,
		},
		"networking": map[string]interface{}{
			"ipv4":         vm.IPv4,
			"mac":          vm.MAC,
			"network_id":   vm.NetworkID,
			"network_name": networkName,
			"tap_device":   vm.TapDevice,
		},
		"assets": map[string]interface{}{
			"image_id":       vm.ImageID,
			"image_name":     imageName,
			"kernel_id":      vm.KernelID,
			"kernel_version": kernelVersion,
			"binary_id":      vm.BinaryID,
			"binary_name":    binaryName,
		},
		"filesystem": map[string]interface{}{
			"vm_dir":             vmDir,
			"rootfs_path":        rootfsPath,
			"config_path":        configPath,
			"log_path":           logPath,
			"serial_output_path": serialPath,
		},
		"console": map[string]interface{}{
			"relay_running":     relayRunning,
			"relay_pid":         relayPID,
			"relay_socket_path": relaySocketPath,
		},
		"volumes": volumes,
	}, nil
}

// ── Start / Stop / Reboot / Pause / Resume ──

// Start starts one or more VMs.
// Matches Python's VMOperation.start() exactly — returns BatchResult[VMInstanceItem].
// Uses batch VMRequest resolution (no N+1), matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (o *VMOperation) Start(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := o.vmRepo
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		o.db,
		o.vmRepo,
		o.enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.start_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}

	for _, v := range resolved.VMs {
		vmLocal := v

		// If VM is stopped, respawn Firecracker process (matches Python's _respawn_firecracker)
		if vmLocal.Status == model.StatusStopped {
			o.respawnFirecracker(ctx, vmLocal, false)
		} else {
			controller, ctrlErr := vm.NewController(vmLocal, repo)
			if ctrlErr != nil {
				results = append(results, errs.OperationResult{
					Status: "error", Code: "vm.start_failed",
					Message:   fmt.Sprintf("start '%s': %v", vmLocal.Name, ctrlErr),
					Exception: ctrlErr,
				})
				continue
			}
			if err := controller.Start(ctx); err != nil {
				results = append(results, errs.OperationResult{
					Status: "error", Code: "vm.start_failed",
					Message:   fmt.Sprintf("start '%s': %v", vmLocal.Name, err),
					Exception: err,
				})
				continue
			}
		}

		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("vm.start", nil, fmt.Sprintf("name=%s", vmLocal.Name))

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.started",
			Item:    vmLocal,
			Message: fmt.Sprintf("VM '%s' started", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

// Stop stops one or more VMs.
// Matches Python's VMOperation.stop() exactly — returns BatchResult[VMInstanceItem].
// Uses batch VMRequest resolution (no N+1), matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (o *VMOperation) Stop(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := o.vmRepo
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		o.db,
		o.vmRepo,
		o.enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.stop_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}

	for _, v := range resolved.VMs {
		vmLocal := v

		controller, ctrlErr := vm.NewController(vmLocal, repo)
		if ctrlErr != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.stop_failed",
				Message:   fmt.Sprintf("stop '%s': %v", vmLocal.Name, ctrlErr),
				Exception: ctrlErr,
			})
			continue
		}
		force := false
		if input.Force != nil {
			force = *input.Force
		}
		controller.Stop(ctx, force)

		// Defense-in-depth: force-kill if stop() silently left the Firecracker process alive
		if vmLocal.PID > 0 && isProcessRunningGo(vmLocal.PID) {
			proc, _ := os.FindProcess(vmLocal.PID)
			if proc != nil {
				_ = proc.Kill()
			}
		}

		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("vm.stop", nil, fmt.Sprintf("name=%s", vmLocal.Name))

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.stopped",
			Item:    vmLocal,
			Message: fmt.Sprintf("VM '%s' stopped", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

func (o *VMOperation) respawnFirecracker(ctx context.Context, v *model.VM, snapshotMode bool) {
	vmDir := filepath.Join(o.cacheDir, "vms", v.ID)

	// ── Restart nocloud-net server if needed (matches Python's respawn_execute) ──
	if v.CloudInitMode == "net" {
		port := 0
		if v.NocloudNetPort != nil {
			port = *v.NocloudNetPort
		}
		if port == 0 || (v.NocloudNetPID != nil && !isProcessRunningGo(*v.NocloudNetPID)) {
			// Resolve network gateway for nocloud URL
			gateway := ""
			if v.Network != nil {
				gateway = v.Network.IPv4Gateway
			}
			if gateway == "" {
				netw, _ := o.netRepo.Get(ctx, v.NetworkID)
				if netw != nil {
					gateway = netw.IPv4Gateway
				}
			}
			if gateway != "" {
				nocloudSvc := nocloudnet.NewNoCloudServer(v.ID, v.Name, vmDir, gateway, port, 8000, 9000, 100)
				if _, newPort, _, startErr := nocloudSvc.Start(ctx, vmDir); startErr != nil {
					slog.Warn("Failed to start/restart nocloud-net server", "vm", v.Name, "error", startErr)
				} else if port == 0 {
					port = newPort
				}
			}
		}
	}

	// ── Force-kill any remaining Firecracker process (matches Python) ──
	if v.PID > 0 && isProcessRunningGo(v.PID) {
		proc, err := os.FindProcess(v.PID)
		if err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			time.Sleep(100 * time.Millisecond)
			if isProcessRunningGo(v.PID) {
				_ = proc.Kill()
				time.Sleep(50 * time.Millisecond)
			}
		}
	}

	// ── Re-ensure TAP device exists before spawning (matches Python) ──
	if v.TapDevice != "" {
		// Resolve network info
		var bridgeName, netID, subnet, gateway string
		if v.Network != nil {
			bridgeName = v.Network.Bridge
			netID = v.Network.ID
			subnet = v.Network.Subnet
			gateway = v.Network.IPv4Gateway
		}
		if bridgeName == "" {
			netw, _ := o.netRepo.Get(ctx, v.NetworkID)
			if netw != nil {
				bridgeName = netw.Bridge
				netID = netw.ID
				subnet = netw.Subnet
				gateway = netw.IPv4Gateway
			}
		}
		if bridgeName != "" {
			netSvc := network.NewService(network.NewRepository(o.db), nil)
			bridgeAddr, calcErr := network.ComputeBridgeAddress(gateway, subnet)
			if calcErr != nil {
				slog.Warn("Failed to compute bridge address during respawn", "vm", v.Name, "error", calcErr)
			} else {
				_ = netSvc.EnsureBridge(ctx, bridgeName, bridgeAddr)
			}
			_ = netSvc.EnsureTap(ctx, v.TapDevice, bridgeName, netID, subnet)
			netSvc.FlushARP(bridgeName)
		}
	}

	// ── Build network config ──
	binaryPath := ""
	if v.BinaryID != "" {
		if v.Binary != nil {
			if v.Binary.Path != "" {
				binaryPath = v.Binary.Path
			}
		}
		if binaryPath == "" {
			bin, err := o.binaryRepo.Get(ctx, v.BinaryID)
			if err == nil && bin != nil {
				binaryPath = bin.Path
			}
		}
	}
	if binaryPath == "" {
		defaultBin, _ := o.binaryRepo.GetDefault(ctx, "firecracker")
		if defaultBin != nil {
			binaryPath = defaultBin.Path
		}
	}
	if binaryPath == "" {
		binaryPath = "/usr/local/bin/firecracker"
	}

	kernelPath := ""
	if v.KernelID != "" {
		if v.Kernel != nil {
			if v.Kernel.Path != "" {
				kernelPath = v.Kernel.Path
			}
		}
		if kernelPath == "" {
			krnl, err := o.kernelRepo.Get(ctx, v.KernelID)
			if err == nil && krnl != nil {
				kernelPath = krnl.Path
			}
		}
	}

	rootfsSuffix := v.RootfsSuffix
	if rootfsSuffix == "" {
		rootfsSuffix = "ext4"
	}
	rootfsPath := filepath.Join(vmDir, "rootfs."+rootfsSuffix)
	if v.RootfsPath != "" {
		rootfsPath = v.RootfsPath
	}

	networkGateway := ""
	if v.NetworkID != "" {
		if v.Network != nil {
			networkGateway = v.Network.IPv4Gateway
		}
		if networkGateway == "" {
			netw, _ := o.netRepo.Get(ctx, v.NetworkID)
			if netw != nil {
				networkGateway = netw.IPv4Gateway
			}
		}
	}

	fcConfig := &model.FirecrackerConfig{
		VMDir:                vmDir,
		RootfsPath:           rootfsPath,
		BinaryPath:           binaryPath,
		KernelPath:           kernelPath,
		VCPUCount:            v.VCPUCount,
		MemSizeMiB:           v.MemSizeMiB,
		GuestIP:              v.IPv4,
		GuestMAC:             v.MAC,
		TapName:              v.TapDevice,
		NetworkGateway:       networkGateway,
		PCIEnabled:           v.PCIEnabled,
		NestedVirt:           v.NestedVirt,
		EnableConsole:        v.EnableConsole,
		EnableLogging:        v.EnableLogging,
		EnableMetrics:        v.EnableMetrics,
		LogLevel:             "Info",
		LogFilename:          "firecracker.log",
		SerialOutputFilename: "serial.out",
		MetricsFilename:      "metrics.log",
		APISocketFilename:    "api.socket",
		PIDFilename:          "firecracker.pid",
		ConfigFilename:       "firecracker.json",
		SnapshotMode:         snapshotMode,
	}
	if v.BootArgs != nil {
		fcConfig.BootArgs = v.BootArgs
	}
	if v.LSMFlags != nil {
		fcConfig.LSMFlags = v.LSMFlags
	}

	// ── Console relay setup (before spawn) ──
	var consoleController *console.Controller
	if v.EnableConsole {
		consoleController = console.NewController(v.ID, vmDir, v.Name,
			"console.pid", "console.sock", "firecracker.console.log")
		ptyFD, ptyErr := consoleController.CreatePTY()
		if ptyErr != nil {
			slog.Warn("Console PTY creation failed during respawn", "vm", v.Name, "error", ptyErr)
		} else {
			fcConfig.RelayEnabled = true
			fcConfig.RelayClientFD = &ptyFD
		}
	}

	spawner := vm.NewFirecrackerSpawner(fcConfig)
	spawner.WriteToFile()

	if err := spawner.Spawn(); err != nil {
		slog.Warn("Failed to respawn Firecracker", "vm", v.Name, "error", err)
		return
	}

	// ── Start console relay after spawn ──
	if consoleController != nil {
		consoleController.CloseClientFD()
		_, _, startErr := consoleController.Start(ctx)
		if startErr != nil {
			slog.Warn("Console relay start failed during respawn", "vm", v.Name, "error", startErr)
		} else {
			slog.Info("Console relay started for VM", "vm", v.Name, "socket", consoleController.SocketPath())
		}
	}

	// Python: targeted DB updates, not full upsert.
	pid := spawner.PID()
	pst := spawner.ProcessStartTime()
	_ = o.vmRepo.UpdateProcessInfo(ctx, v.ID, pid, pst)

	newStatus := model.StatusRunning
	if snapshotMode {
		newStatus = model.StatusPaused
	}
	_ = o.vmRepo.UpdateStatus(ctx, v.ID, newStatus)

	// Update in-memory VM object
	v.PID = safeDerefInt(pid)
	v.ProcessStartTime = pst
	v.Status = newStatus
}

// ── Snapshot / Load ──

// Snapshot creates a snapshot of a single VM (matches Python's VMOperation.snapshot() exactly).
// Python resolves exactly one VM, returns item=vm in all cases (success, error, failure).
// memFile and stateFile are output paths for the snapshot files (matches Python's mem_out, state_out).
func (o *VMOperation) Snapshot(ctx context.Context, input *inputs.VMInput, memFile string, stateFile string) *errs.OperationResult {
	// Python: resolved = VMRequest(inputs=inputs, db=Database()).resolve()
	//         if len(resolved.vms) != 1: raise VMNotFoundError
	vmItem, err := o.resolveSingleVM(ctx, input.Identifiers[0])
	if err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMNotFound),
			Message: fmt.Sprintf("VM not found: %s", input.Identifiers[0]),
		}
	}

	controller, ctrlErr := vm.NewController(vmItem, o.vmRepo)
	if ctrlErr != nil {
		return &errs.OperationResult{
			Status: "error", Code: "vm.snapshot_failed",
			Item:      vmItem,
			Message:   fmt.Sprintf("Failed to snapshot VM '%s': %v", vmItem.Name, ctrlErr),
			Exception: ctrlErr,
		}
	}
	if err := controller.Snapshot(ctx, memFile, stateFile); err != nil {
		// Python catches MVMError → status="error", Exception → status="failure", item=vm
		status := "error"
		var de *errs.DomainError
		if !errors.As(err, &de) {
			status = "failure"
		}
		return &errs.OperationResult{
			Status: status, Code: "vm.snapshot_failed",
			Item:      vmItem,
			Message:   fmt.Sprintf("Failed to snapshot VM '%s': %v", vmItem.Name, err),
			Exception: err,
		}
	}

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("vm.snapshot", nil, fmt.Sprintf("name=%s", vmItem.Name))

	return &errs.OperationResult{
		Status: "success", Code: "vm.snapshot_created",
		Item:    vmItem,
		Message: fmt.Sprintf("VM '%s' snapshot saved", vmItem.Name),
	}
}

// Load loads (resumes from snapshot) a single VM.
// memFile and stateFile are input snapshot file paths; resume controls whether VM starts after load.
// Matches Python's VMOperation.load_snapshot() exactly:
//   - re-reads VM after respawn (Python: repo.get(vm.id) → updated)
//   - catches MVMError → status="error", Exception → status="failure", item=vm
func (o *VMOperation) Load(ctx context.Context, input *inputs.VMInput, memFile string, stateFile string, resume bool) *errs.OperationResult {
	repo := o.vmRepo

	// Validate only one VM for load (matches Python's exactly one VM identifier check)
	if len(input.Identifiers) != 1 {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMNotFound),
			Message: "Expected exactly one VM identifier",
		}
	}

	memFilePath := memFile
	stateFilePath := stateFile

	// Validate snapshot files exist before loading (matches Python's exists() checks)
	var missing []string
	if _, err := os.Stat(memFilePath); err != nil {
		if os.IsNotExist(err) {
			missing = append(missing, memFilePath)
		}
	}
	if _, err := os.Stat(stateFilePath); err != nil {
		if os.IsNotExist(err) {
			missing = append(missing, stateFilePath)
		}
	}
	if len(missing) > 0 {
		paths := strings.Join(missing, ", ")
		return &errs.OperationResult{
			Status: "error", Code: "vm.load_snapshot_failed",
			Message: fmt.Sprintf("Snapshot file(s) not found: %s", paths),
		}
	}

	vmItem, err := o.resolveSingleVM(ctx, input.Identifiers[0])
	if err != nil {
		return &errs.OperationResult{
			Status: "error", Code: "vm.load_snapshot_failed",
			Item:    nil,
			Message: fmt.Sprintf("VM not found: %s", input.Identifiers[0]),
		}
	}

	// If the VM is stopped, spawn a fresh Firecracker in pre-boot (snapshot) mode
	// so the API socket is available for PUT /snapshot/load (matches Python logic).
	// Python: if vm.status == VMStatus.STOPPED.value: _respawn_firecracker(vm, snapshot_mode=True)
	//         repo = Repository(Database()); updated = repo.get(vm.id); if updated: vm = updated
	if vmItem.Status == model.StatusStopped {
		o.respawnFirecracker(ctx, vmItem, true)
		// Python re-reads the updated vm from DB after respawn:
		updated, getErr := repo.Get(ctx, vmItem.ID)
		if getErr == nil && updated != nil {
			vmItem = updated
		}
	}

	// Python's try block starts here
	status := "success"
	code := "vm.snapshot_loaded"
	msg := fmt.Sprintf("Snapshot loaded for VM '%s'", vmItem.Name)
	var resultItem interface{} = vmItem
	var exception error

	controller, ctrlErr := vm.NewController(vmItem, repo)
	if ctrlErr != nil {
		status = "error"
		code = "vm.load_snapshot_failed"
		msg = fmt.Sprintf("Failed to load snapshot for VM '%s': %v", vmItem.Name, ctrlErr)
		exception = ctrlErr
		resultItem = vmItem
	} else if err := controller.LoadSnapshot(ctx, memFilePath, stateFilePath, resume); err != nil {
		// Python catches MVMError → status="error", Exception → status="failure", item=vm
		status = "error"
		var de *errs.DomainError
		if !errors.As(err, &de) {
			status = "failure"
		}
		code = "vm.load_snapshot_failed"
		msg = fmt.Sprintf("Failed to load snapshot for VM '%s': %v", vmItem.Name, err)
		exception = err
		resultItem = vmItem
	} else {
		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("vm.load", nil, fmt.Sprintf("name=%s", vmItem.Name))
	}

	return &errs.OperationResult{
		Status: status, Code: code,
		Item:      resultItem,
		Message:   msg,
		Exception: exception,
	}
}

// ── Reboot / Pause / Resume ──

// Reboot reboots one or more VMs.
// Matches Python's VMOperation.reboot() exactly — returns BatchResult[VMInstanceItem].
// Uses batch VMRequest resolution (no N+1), matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (o *VMOperation) Reboot(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := o.vmRepo
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		o.db,
		o.vmRepo,
		o.enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.reboot_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}

	for _, v := range resolved.VMs {
		vmLocal := v

		// Stop the VM first (kills the firecracker process)
		controller, ctrlErr := vm.NewController(vmLocal, repo)
		if ctrlErr != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.reboot_failed",
				Message:   fmt.Sprintf("reboot '%s': %v", vmLocal.Name, ctrlErr),
				Exception: ctrlErr,
			})
			continue
		}
		force := false
		if input.Force != nil {
			force = *input.Force
		}
		controller.Stop(ctx, force)

		// After stop, respawn a fresh firecracker process
		o.respawnFirecracker(ctx, vmLocal, false)

		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("vm.reboot", nil, fmt.Sprintf("name=%s", vmLocal.Name))

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.rebooted",
			Item:    vmLocal,
			Message: fmt.Sprintf("VM '%s' rebooted", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

// Pause pauses one or more VMs.
// Matches Python's VMOperation.pause() exactly — returns BatchResult[VMInstanceItem].
// Uses batch VMRequest resolution (no N+1), matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (o *VMOperation) Pause(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := o.vmRepo
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		o.db,
		o.vmRepo,
		o.enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.pause_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}

	for _, v := range resolved.VMs {
		vmLocal := v

		controller, ctrlErr := vm.NewController(vmLocal, repo)
		if ctrlErr != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.pause_failed",
				Message:   fmt.Sprintf("pause '%s': %v", vmLocal.Name, ctrlErr),
				Exception: ctrlErr,
			})
			continue
		}
		if err := controller.Pause(ctx); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.pause_failed",
				Message:   fmt.Sprintf("pause '%s': %v", vmLocal.Name, err),
				Exception: err,
			})
			continue
		}

		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("vm.pause", nil, fmt.Sprintf("name=%s", vmLocal.Name))

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.paused",
			Item:    vmLocal,
			Message: fmt.Sprintf("VM '%s' paused", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

// Resume resumes one or more VMs.
// Matches Python's VMOperation.resume() exactly — returns BatchResult[VMInstanceItem].
// Uses batch VMRequest resolution (no N+1), matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (o *VMOperation) Resume(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := o.vmRepo
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		o.db,
		o.vmRepo,
		o.enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.resume_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}

	for _, v := range resolved.VMs {
		vmLocal := v

		controller, ctrlErr := vm.NewController(vmLocal, repo)
		if ctrlErr != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.resume_failed",
				Message:   fmt.Sprintf("resume '%s': %v", vmLocal.Name, ctrlErr),
				Exception: ctrlErr,
			})
			continue
		}
		if err := controller.Resume(ctx); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.resume_failed",
				Message:   fmt.Sprintf("resume '%s': %v", vmLocal.Name, err),
				Exception: err,
			})
			continue
		}

		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("vm.resume", nil, fmt.Sprintf("name=%s", vmLocal.Name))

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.resumed",
			Item:    vmLocal,
			Message: fmt.Sprintf("VM '%s' resumed", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

// ── AttachVolume / DetachVolume ──

// AttachVolume attaches a volume to a VM.
// Matches Python's VMOperation.attach_volume() exactly:
//   - VMInput for identification (name, ID, IP, MAC)
//   - VolumeResolver for volume resolution
//   - Version gate for hotplug
//   - VolumeController.attach + VM volume_ids update
func (o *VMOperation) AttachVolume(ctx context.Context, input *inputs.VMInput, volumeName string) *errs.OperationResult {
	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "attach volume"); err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodePrivilegeRequired),
			Message:   fmt.Sprintf("Privilege check failed: %v", err),
			Exception: err,
		}
	}

	// Resolve VM using VMRequest pipeline (matches Python: VMRequest(inputs=vm_inputs, db=db).resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		o.db,
		o.vmRepo,
		o.enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMNotFound),
			Message: fmt.Sprintf("VM not found: %v", resolveErr),
		}
	}
	if len(resolved.VMs) != 1 {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMNotFound),
			Message: "Expected exactly one VM identifier",
		}
	}
	vmItem := resolved.VMs[0]

	// Resolve volume using VolumeResolver (matches Python: vol_resolver.resolve(volume_name))
	volResolver := volume.NewResolver(o.volRepo)
	vol, err := volResolver.Resolve(ctx, volumeName)
	if err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVolumeNotFound),
			Message: fmt.Sprintf("Volume '%s' not found", volumeName),
		}
	}

	// Check volume status (matches Python: if vol.status != VolumeStatus.AVAILABLE: raise VMCreateError(...))
	if vol.Status != model.VolumeStatusAvailable {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMCreateFailed),
			Message: fmt.Sprintf("Volume '%s' is not available", volumeName),
		}
	}

	// Hotplug on running VM (matches Python: if vm.status == VMStatus.RUNNING)
	if vmItem.Status == model.StatusRunning {
		// Version gate: hotplug requires Firecracker v1.16+ (matches Python's VersionGate.require)
		if vmItem.BinaryID != "" {
			bin, _ := o.binaryRepo.Get(ctx, vmItem.BinaryID)
			if bin != nil && bin.Version != "" {
				if !isFirecrackerVersionAtLeast(bin.Version, "1.16") {
					return &errs.OperationResult{
						Status: "error",
						Code:   string(errs.CodeBinaryVersionGate),
						Item:   vmItem,
						Message: fmt.Sprintf("Volume hotplug requires Firecracker >= 1.16, got %s. Use a newer Firecracker binary or attach the volume while the VM is stopped.",
							bin.Version),
					}
				}
			}
		}
		// Try Firecracker API hotplug (matches Python's try: controller.attach_volume(vol) except Exception: logger.warning)
		controller, ctrlErr := vm.NewController(vmItem, o.vmRepo)
		if ctrlErr == nil {
			if err := controller.AttachVolume(ctx, vol); err != nil {
				slog.Warn("Hotplug failed for drive", "volume", vol.ID, "error", err)
			}
		}
	}

	// VolumeController.attach (matches Python's vol_controller = VolumeController(vol, vol_repo); vol_controller.attach(vm.id))
	volController, volCtrlErr := volume.NewController(ctx, vol, o.volRepo)
	if volCtrlErr == nil {
		_ = volController.Attach(ctx, vmItem.ID)
	}

	// Update VM's volume_ids (matches Python's list comprehension + append-if-not-present)
	vmVolumeIDs := make([]string, 0)
	if len(vmItem.VolumeIDs) > 0 {
		vmVolumeIDs = vmItem.VolumeIDs
	}
	found := false
	for _, id := range vmVolumeIDs {
		if id == vol.ID {
			found = true
			break
		}
	}
	if !found {
		vmVolumeIDs = append(vmVolumeIDs, vol.ID)
	}
	vmItem.VolumeIDs = vmVolumeIDs
	_ = o.vmRepo.Upsert(ctx, vmItem)

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("vm.attach_volume", map[string]interface{}{
		"vm": vmItem.Name, "volume": vol.Name,
	}, "")

	return &errs.OperationResult{
		Status: "success", Code: "vm.volume_attached",
		Item:    vmItem,
		Message: fmt.Sprintf("Volume '%s' attached to VM '%s'", volumeName, vmItem.Name),
	}
}

// DetachVolume detaches a volume from a VM.
// Matches Python's VMOperation.detach_volume() exactly:
//   - VMInput for identification (name, ID, IP, MAC)
//   - VolumeResolver for volume resolution
//   - Version gate + SSH PCI removal + Firecracker API for hot-unplug
//   - VolumeController.detach + VM volume_ids update
func (o *VMOperation) DetachVolume(ctx context.Context, input *inputs.VMInput, volumeName string) *errs.OperationResult {
	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "detach volume"); err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodePrivilegeRequired),
			Message:   fmt.Sprintf("Privilege check failed: %v", err),
			Exception: err,
		}
	}

	// Resolve VM using VMRequest pipeline (matches Python: VMRequest(inputs=vm_inputs, db=db).resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		o.db,
		o.vmRepo,
		o.enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMNotFound),
			Message: fmt.Sprintf("VM not found: %v", resolveErr),
		}
	}
	if len(resolved.VMs) != 1 {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMNotFound),
			Message: "Expected exactly one VM identifier",
		}
	}
	vmItem := resolved.VMs[0]

	// Resolve volume using VolumeResolver (matches Python: vol_resolver.resolve(volume_name))
	volResolver := volume.NewResolver(o.volRepo)
	vol, err := volResolver.Resolve(ctx, volumeName)
	if err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodeVolumeNotFound),
			Message: fmt.Sprintf("Volume '%s' not found", volumeName),
		}
	}

	// Hot-unplug if running (matches Python: if vm.status == VMStatus.RUNNING)
	if vmItem.Status == model.StatusRunning {
		// Version gate: hot-unplug requires Firecracker v1.16+ (matches Python's VersionGate.require)
		if vmItem.BinaryID != "" {
			bin, _ := o.binaryRepo.Get(ctx, vmItem.BinaryID)
			if bin != nil && bin.Version != "" {
				if !isFirecrackerVersionAtLeast(bin.Version, "1.16") {
					return &errs.OperationResult{
						Status: "error",
						Code:   string(errs.CodeBinaryVersionGate),
						Item:   vmItem,
						Message: fmt.Sprintf("Volume hot-unplug requires Firecracker >= 1.16, got %s. Use a newer Firecracker binary or detach the volume while the VM is stopped.",
							bin.Version),
					}
				}
			}
		}

		// Step 1: SSH into guest and remove the PCI device (matches Python's SSH PCI removal)
		if len(vmItem.SSHKeys) > 0 && vmItem.IPv4 != "" {
			// Resolve the first SSH key to get private key path (matches Python's KeyResolver().by_id())
			sshKey, keyErr := o.keyRepo.GetByName(ctx, vmItem.SSHKeys[0])
			if keyErr != nil || sshKey == nil {
				matches, matchErr := o.keyRepo.FindByPrefix(ctx, vmItem.SSHKeys[0])
				if matchErr == nil && len(matches) > 0 {
					sshKey = matches[0]
					keyErr = nil
				} else {
					keyErr = fmt.Errorf("key not found: %s", vmItem.SSHKeys[0])
				}
			}
			if keyErr != nil || sshKey == nil {
				slog.Warn("SSH PCI removal: could not resolve SSH key", "key", vmItem.SSHKeys[0])
			} else {
				keyPath := ""
				if sshKey.PrivateKeyPath != nil {
					keyPath = *sshKey.PrivateKeyPath
				}
				sshUser := "root"
				if vmItem.SSHUser != nil && *vmItem.SSHUser != "" {
					sshUser = *vmItem.SSHUser
				}
				timeout := 10

				// Find the last Virtio block device BDF (the hotplugged one) using tail -1 to skip root device
				// Matches Python: lspci -D | grep 'Virtio.*block' | tail -1 | awk '{print $1}'
				sshSvc, svcErr := ssh.NewService(vmItem.IPv4, sshUser, keyPath, &timeout)
				if svcErr != nil {
					slog.Warn("SSH PCI removal: failed to create SSH service", "error", svcErr)
				} else {
					findBDFCmd := sshSvc.BuildCommand("lspci -D | grep 'Virtio.*block' | tail -1 | awk '{print $1}'")
					bdfResult := system.RunCmdCompat(ctx, findBDFCmd, system.RunCmdOptions{Capture: true, Check: false})
					bdf := ""
					if bdfResult.Err == nil {
						bdf = strings.TrimSpace(bdfResult.Stdout)
					}

					if bdf != "" {
						removeCmd := sshSvc.BuildCommand(fmt.Sprintf("echo 1 > /sys/bus/pci/devices/%s/remove", bdf))
						_ = system.RunCmdCompat(ctx, removeCmd, system.RunCmdOptions{Capture: false, Check: false})
						slog.Info("Removed PCI device from guest for drive", "bdf", bdf, "volume", vol.ID)
					}
				}
			}
		}

		// Step 2: Call Firecracker API to delete the drive (matches Python's controller.detach_volume)
		controller, ctrlErr := vm.NewController(vmItem, o.vmRepo)
		if ctrlErr == nil {
			if err := controller.DetachVolume(ctx, vol); err != nil {
				slog.Warn("Firecracker delete_drive failed", "volume", vol.ID, "error", err)
			}
		}
	}

	// VolumeController.detach (matches Python's vol_controller = VolumeController(vol, vol_repo); vol_controller.detach())
	volController, volCtrlErr := volume.NewController(ctx, vol, o.volRepo)
	if volCtrlErr == nil {
		_ = volController.Detach(ctx)
	}

	// Update VM's volume_ids (matches Python's list comprehension + remove-if-present)
	vmVolumeIDs := make([]string, 0)
	if len(vmItem.VolumeIDs) > 0 {
		vmVolumeIDs = vmItem.VolumeIDs
	}
	newIDs := make([]string, 0, len(vmVolumeIDs))
	for _, id := range vmVolumeIDs {
		if id != vol.ID {
			newIDs = append(newIDs, id)
		}
	}
	vmItem.VolumeIDs = newIDs
	_ = o.vmRepo.Upsert(ctx, vmItem)

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("vm.detach_volume", map[string]interface{}{
		"vm": vmItem.Name, "volume": vol.Name,
	}, "")

	return &errs.OperationResult{
		Status: "success", Code: "vm.volume_detached",
		Item:    vmItem,
		Message: fmt.Sprintf("Volume '%s' detached from VM '%s'", volumeName, vmItem.Name),
	}
}

// ── Import / Export ──

// Import creates a VM from a portable export config file.
// Matches Python's VMOperation.import_() exactly:
//   - Reads VMExportConfig JSON from input.ConfigPath
//   - Uses VMImportRequest to resolve semantic references
//   - Delegates to VMCreateBuilder for full resolution
//   - Delegates to executeCreate for provisioning
//   - Matches Python's try/except MVMError → "error", Exception → "failure"
func (o *VMOperation) Import(ctx context.Context, input *inputs.VMImportInput, onProgress func(errs.ProgressEvent)) *errs.OperationResult {
	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "import VM"); err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodePrivilegeRequired),
			Message:   fmt.Sprintf("Privilege check failed: %v", err),
			Exception: err,
		}
	}

	// Python wraps the resolve+create in a try/except that catches MVMError and Exception.
	var execErr error
	var resolved *inputs.VMCreateResolved
	var vmInstance *model.VM

	// Use VMImportRequest for full semantic resolution pipeline
	// (matches Python: VMImportRequest(inputs=inputs, db=db).resolve())
	request := inputs.NewVMImportRequest(*input, o.db)
	resolved, execErr = request.Resolve(ctx)
	if execErr == nil {
		// Set up signal-based cleanup (matches Python's SigtermContext(lambda: ctx.cleanup()))
		createCtx, cancelCreate := context.WithCancel(ctx)
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

		var vmCleanup func()

		go func() {
			select {
			case <-sigCh:
				slog.Warn("Received termination signal during VM import, cleaning up...", "vm", resolved.Name)
				if vmCleanup != nil {
					vmCleanup()
				}
				cancelCreate()
			case <-createCtx.Done():
			}
		}()
		signalCleanup := func() {
			signal.Stop(sigCh)
			close(sigCh)
			cancelCreate()
		}

		// Convert VMCreateResolved to internal resolvedVMCreateInput for executeCreate
		internalResolved := resolvedFromBuilderOutput(resolved)
		if internalResolved == nil {
			signalCleanup()
			execErr = fmt.Errorf("Failed to convert resolved input")
		} else {
			vmInstance, execErr = o.executeCreate(createCtx, internalResolved, onProgress, &vmCleanup)
			signalCleanup()
			if execErr != nil {
				if internalResolved.SkipCleanup {
					slog.Warn("VM import failed but --skip-cleanup is active", "dir", internalResolved.VMDir)
				}
			} else {
				// Handle volumes
				if len(internalResolved.Volumes) > 0 {
					volSvc := volume.NewService(o.volRepo)
					volSvc.SetVolumesState(ctx, internalResolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
					vmInstance.VolumeIDs = make([]string, len(internalResolved.Volumes))
					for i, v := range internalResolved.Volumes {
						vmInstance.VolumeIDs[i] = v.ID
					}
					o.vmRepo.Upsert(ctx, vmInstance)
				}
			}
		}
	}

	if execErr != nil {
		// Python's try/except MVMError (DomainError) → status="error", Exception → status="failure"
		// Uses var de *DomainError + errors.As pattern matching all other existing API methods.
		status := "error"
		var de *errs.DomainError
		if !errors.As(execErr, &de) {
			status = "failure"
		}
		return &errs.OperationResult{
			Status: status, Code: "vm.import_failed",
			Message:   execErr.Error(),
			Exception: execErr,
		}
	}

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("vm.import", nil, fmt.Sprintf("name=%s,config=%s", resolved.Name, input.ConfigPath))

	return &errs.OperationResult{
		Status: "success", Code: "vm.imported",
		Item:    []*model.VM{vmInstance},
		Message: fmt.Sprintf("VM imported from %s", input.ConfigPath),
	}
}

// Export exports a VM's configuration as a portable VMExportConfig.
// Matches Python's VMOperation.export() exactly — returns VMExportConfig, not an error code.
func (o *VMOperation) Export(ctx context.Context, input *inputs.VMInput) (*inputs.VMExportConfig, error) {
	vmItem, err := o.Get(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}

	// Resolve related asset metadata (matches Python's Repository(db).get(vm.image_id) etc.)
	imageRepo := image.NewRepository(o.db)
	kernelRepo := kernel.NewRepository(o.db)
	binaryRepo := binary.NewRepository(o.db)
	netRepo := network.NewRepository(o.db)

	image, _ := imageRepo.Get(ctx, vmItem.ImageID)
	kernel, _ := kernelRepo.Get(ctx, vmItem.KernelID)
	binary, _ := binaryRepo.Get(ctx, vmItem.BinaryID)
	netItem, _ := netRepo.Get(ctx, vmItem.NetworkID)

	diskSize := ""
	if vmItem.DiskSizeMiB > 0 {
		diskSize = fmt.Sprintf("%dM", vmItem.DiskSizeMiB)
	}

	binName := "firecracker"
	binVersion := ""
	if binary != nil {
		binName = binary.Name
		binVersion = binary.Version
	}

	bootArgsStr := ""
	if vmItem.BootArgs != nil {
		bootArgsStr = *vmItem.BootArgs
	}

	lsmFlags := ""
	if vmItem.LSMFlags != nil {
		lsmFlags = *vmItem.LSMFlags
	}

	nocloudPort := 0
	if vmItem.NocloudNetPort != nil {
		nocloudPort = *vmItem.NocloudNetPort
	}

	// Convert string values to *string for export config (which uses str | None)
	var imageType, imageArch *string
	if image != nil {
		imageType = &image.Type
		imageArch = &image.Arch
	}
	var diskSizePtr *string
	if diskSize != "" {
		diskSizePtr = &diskSize
	}
	var kernelVersion, kernelArch, kernelType *string
	if kernel != nil {
		kernelVersion = &kernel.Version
		kernelArch = &kernel.Arch
		kernelType = &kernel.Type
	}
	var binVersionPtr *string
	if binVersion != "" {
		binVersionPtr = &binVersion
	}
	var netName, netSubnet, netGateway *string
	var netNATGateways string
	netNATEnabled := false
	if netItem != nil {
		netName = &netItem.Name
		netSubnet = &netItem.Subnet
		netGateway = &netItem.IPv4Gateway
		gws := network.NatGatewaysList(netItem)
		netNATGateways = strings.Join(gws, ",")
		netNATEnabled = netItem.NATEnabled
	}
	var ipPtr, macPtr *string
	if vmItem.IPv4 != "" {
		ipPtr = &vmItem.IPv4
	}
	if vmItem.MAC != "" {
		macPtr = &vmItem.MAC
	}

	// Convert cpu_config to JSON string (matches Python: json.dumps(vm.cpu_config) if isinstance(vm.cpu_config, dict) else vm.cpu_config)
	var cpuConfigStr *string
	if vmItem.CPUConfig != nil {
		if data, err := json.Marshal(vmItem.CPUConfig); err == nil {
			s := string(data)
			cpuConfigStr = &s
		} else {
			s := fmt.Sprintf("%v", vmItem.CPUConfig)
			cpuConfigStr = &s
		}
	}

	// Convert remaining string/primitive values to pointer types for export config
	var lsmFlagsPtr *string
	if lsmFlags != "" {
		lsmFlagsPtr = &lsmFlags
	}
	var cloudInitModePtr *string
	if vmItem.CloudInitMode != "" {
		cloudInitModePtr = &vmItem.CloudInitMode
	}
	rootUser := "root"

	cfg := &inputs.VMExportConfig{
		SchemaVersion: "1.0",
		Name:          vmItem.Name,
		Compute: inputs.VMExportComputeConfig{
			VCPUs: intPtr(vmItem.VCPUCount),
			Mem:   intPtr(vmItem.MemSizeMiB),
		},
		Image: inputs.VMExportImageConfig{
			Type:     imageType,
			Arch:     imageArch,
			DiskSize: diskSizePtr,
		},
		Kernel: inputs.VMExportKernelConfig{
			Version: kernelVersion,
			Arch:    kernelArch,
			Type:    kernelType,
		},
		Binary: inputs.VMExportBinaryConfig{
			Name:    binName,
			Version: binVersionPtr,
		},
		Network: inputs.VMExportNetworkConfig{
			Name:        netName,
			Subnet:      netSubnet,
			IPv4Gateway: netGateway,
			NATGateways: &netNATGateways,
			NATEnabled:  &netNATEnabled,
			IP:          ipPtr,
			MAC:         macPtr,
		},
		Boot: inputs.VMExportBootConfig{
			Args:          strPtr(bootArgsStr),
			EnableConsole: &vmItem.EnableConsole,
		},
		Firecracker: inputs.VMExportFirecrackerConfig{
			EnableAPISocket: boolPtr(true),
			PCIEnabled:      &vmItem.PCIEnabled,
			LsmFlags:        lsmFlagsPtr,
			NestedVirt:      &vmItem.NestedVirt,
			CPUConfig:       cpuConfigStr,
		},
		CloudInit: inputs.VMExportCloudInitConfig{
			Mode:           cloudInitModePtr,
			User:           &rootUser,
			NocloudNetPort: intPtr(nocloudPort),
		},
	}

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("vm.export", map[string]interface{}{"name": vmItem.Name}, "")

	return cfg, nil
}

func boolPtr(b bool) *bool { return &b }

// ── Internal helpers ──

type resolvedVMCreateInput struct {
	Name                  string
	VMID                  string
	VMDir                 string
	VCPUCount             int
	MemSizeMiB            int
	User                  string
	DNSServer             string
	RootUID               int
	RootGID               int
	UserUID               int
	UserGID               int
	GuestMACPrefix        string
	Network               *model.Network
	Image                 *model.ImageItem
	Kernel                *model.KernelItem
	Binary                *model.BinaryItem
	NetworkPrefixLen      int
	CloudInitMode         model.CloudInitMode
	SkipCINetworkConfig   bool
	PCIEnabled            bool
	NestedVirt            bool
	EnableConsole         bool
	EnableLogging         bool
	EnableMetrics         bool
	KeepCloudInitISO      bool
	SkipCleanup           bool
	SkipDeblob            bool
	NetworkNetmask        string
	DiskSizeBytes         int64
	DiskSizeMiB           int
	LSMFlags              string
	LogLevel              string
	LogFilename           string
	SerialOutputFilename  string
	MetricsFilename       string
	APISocketFilename     string
	PIDFilename           string
	ConfigFilename        string
	ConsoleSocketFilename string
	ConsolePIDFilename    string
	CloudInitISOName      string
	NocloudPortRangeStart int
	NocloudPortRangeEnd   int
	NocloudMaxPortRetries int
	RequestedGuestIP      *string
	RequestedGuestMAC     *string
	NocloudNetPort        *int
	CustomUserDataPath    *string
	CloudInitISOPath      *string
	CPUConfig             *model.CpuConfig
	BootArgs              string
	SSHKeys               []string
	Provisioner           model.ProvisionerType
	ExtraDrives           []model.DriveConfig
	Volumes               []*model.VolumeItem
}

type vmCreateContext struct {
	name             string
	vmID             string
	vmDir            string
	guestIP          string
	guestMAC         string
	tapName          string
	rootfsPath       string
	onProgress       func(errs.ProgressEvent)
	resolved         *resolvedVMCreateInput
	fcManager        *model.FirecrackerConfig
	spawner          *vm.FirecrackerSpawner
	relay            *console.Controller
	cloudInitResult  *cloudInitResult
	resourcesCreated map[string]bool
	cacheDir         string
	db               *sql.DB
	// Fields for respawn flow (matches Python's _vm, _snapshot_mode)
	_vm           *model.VM
	_snapshotMode bool
}

type cloudInitResult struct {
	mode              model.CloudInitMode
	isoPath           *string
	nocloudURL        *string
	nocloudPort       *int
	nocloudPID        *int
	nocloudNetManager any // any because type depends on cloud-init mode (server or ISO) — concrete typing not feasible
	nocloudNetRules   []any
}

func (c *vmCreateContext) execute(ctx context.Context) error {
	if c.vmDir == "" {
		return fmt.Errorf("VM directory not set in context")
	}
	if c.resolved == nil {
		return fmt.Errorf("Failed to resolve necessary dependencies")
	}

	// Generate MAC and TAP name
	if c.resolved.RequestedGuestMAC != nil {
		c.guestMAC = *c.resolved.RequestedGuestMAC
	} else {
		c.guestMAC = VMGenerateMAC(c.resolved.GuestMACPrefix)
	}
	c.tapName = VMGenerateTAPName(c.resolved.Network.Name, c.resolved.Name)

	// Create VM directory
	if err := os.MkdirAll(c.vmDir, 0755); err != nil {
		return fmt.Errorf("create VM directory: %w", err)
	}
	c.markCreated("vm_dir")

	// Progress: network
	if c.onProgress != nil {
		c.onProgress(errs.ProgressEvent{Phase: "network", Status: "running", Message: "Configuring network..."})
	}

	// Network setup
	netSvc := network.NewService(network.NewRepository(nil), nil)
	bridgeAddr, calcErr := network.ComputeBridgeAddress(c.resolved.Network.IPv4Gateway, c.resolved.Network.Subnet)
	if calcErr != nil {
		return fmt.Errorf("compute bridge address: %w", calcErr)
	}
	if err := netSvc.EnsureBridge(ctx, c.resolved.Network.Bridge, bridgeAddr); err != nil {
		return fmt.Errorf("ensure bridge: %w", err)
	}

	leaseSvc, err := network.NewLeaseService(c.resolved.Network, network.NewLeaseRepository(c.db), nil)
	if err != nil {
		return fmt.Errorf("create lease service: %w", err)
	}
	if c.resolved.RequestedGuestIP != nil {
		ip, err := leaseSvc.LeaseSpecific(ctx, *c.resolved.RequestedGuestIP, c.vmID)
		if err != nil {
			return fmt.Errorf("lease specific IP: %w", err)
		}
		c.guestIP = ip
	} else {
		ip, err := leaseSvc.Lease(ctx, c.vmID)
		if err != nil {
			return fmt.Errorf("lease IP: %w", err)
		}
		c.guestIP = ip
	}

	natGateways := network.NatGatewaysList(c.resolved.Network)
	if c.resolved.Network.NATEnabled && len(natGateways) > 0 {
		_ = netSvc.EnsureNAT(ctx, c.resolved.Network.Bridge, natGateways,
			c.resolved.Network.Subnet, c.resolved.Network.ID)
	}

	if err := netSvc.EnsureTap(ctx, c.tapName, c.resolved.Network.Bridge,
		c.resolved.Network.ID, c.resolved.Network.Subnet); err != nil {
		return fmt.Errorf("ensure TAP: %w", err)
	}
	c.markCreated("network_tap")
	netSvc.FlushARP(c.resolved.Network.Bridge)

	// Progress: rootfs
	if c.onProgress != nil {
		c.onProgress(errs.ProgressEvent{Phase: "rootfs", Status: "running", Message: "Copying root filesystem..."})
	}

	// Clone rootfs
	if err := c.cloneImage(); err != nil {
		return err
	}
	c.markCreated("rootfs")

	// Progress: cloud-init
	if c.onProgress != nil {
		c.onProgress(errs.ProgressEvent{Phase: "cloud-init", Status: "running", Message: "Provisioning cloud-init..."})
	}

	// --- Cloud-init provisioning ---
	// Build Provisioner matching Python's Provisioner(...) with all params
	provisioner, err := vm.NewProvisioner(
		c.rootfsPath,
		provisioner.ProvisionerType(c.resolved.Provisioner),
		c.resolved.Image.FSType,
		vm.WithRootUID(c.resolved.RootUID),
		vm.WithRootGID(c.resolved.RootGID),
		vm.WithUserUID(c.resolved.UserUID),
		vm.WithUserGID(c.resolved.UserGID),
	)
	if err != nil {
		return fmt.Errorf("failed to create VM provisioner: %w", err)
	}

	// Resize rootfs
	provisioner.Resize(c.resolved.DiskSizeBytes)

	mode := c.resolved.CloudInitMode

	// Read SSH pubkeys from the key service (used by OFF, INJECT, ISO, NET modes)
	keySvc := key.NewService(key.NewRepository(nil))
	pubkeys, _ := keySvc.GetPubkeys(ctx, c.resolved.SSHKeys, filepath.Join(c.cacheDir, "keys"))

	// Common operations for OFF and INJECT modes
	if mode == model.CloudInitModeOFF || mode == model.CloudInitModeINJECT {
		provisioner.SetHostname(c.resolved.Name)
		provisioner.InjectDNS(c.resolved.DNSServer)
		provisioner.SetupSSH(c.resolved.User, pubkeys)
	}

	if mode == model.CloudInitModeOFF {
		provisioner.DisableCloudInit()
		c.markCreated("cloud-init-off")

	} else if mode == model.CloudInitModeINJECT {
		ciConfig := &model.ProvisionConfig{
			Mode:                  mode,
			VMName:                c.resolved.Name,
			VMID:                  c.vmID,
			VMDir:                 c.vmDir,
			CloudInitDir:          filepath.Join(c.vmDir, "cloud-init"),
			GuestIP:               c.guestIP,
			TapName:               c.tapName,
			User:                  c.resolved.User,
			IPv4Gateway:           c.resolved.Network.IPv4Gateway,
			NetworkPrefixLen:      c.resolved.NetworkPrefixLen,
			SkipNetworkConfig:     c.resolved.SkipCINetworkConfig,
			SSHPubkeys:            pubkeys,
			CustomUserDataPath:    c.resolved.CustomUserDataPath,
			NocloudNetPort:        c.resolved.NocloudNetPort,
			CloudInitISOPath:      c.resolved.CloudInitISOPath,
			KeepCloudInitISO:      c.resolved.KeepCloudInitISO,
			CloudInitISOName:      c.resolved.CloudInitISOName,
			NocloudPortRangeStart: c.resolved.NocloudPortRangeStart,
			NocloudPortRangeEnd:   c.resolved.NocloudPortRangeEnd,
			NocloudMaxPortRetries: c.resolved.NocloudMaxPortRetries,
		}
		ciProvisioner := cloudinit.NewProvisioner(ciConfig, nil)
		ciResult, ciErr := ciProvisioner.Provision(ctx)
		if ciErr != nil {
			return fmt.Errorf("cloud-init inject provisioning failed: %w", ciErr)
		}
		c.cloudInitResult = &cloudInitResult{
			mode: ciResult.Mode,
		}
		provisioner.InjectCloudInit(ciConfig.CloudInitDir)
		c.markCreated("cloud-init-inject")

	} else if mode == model.CloudInitModeISO || mode == model.CloudInitModeNET {
		ciConfig := &model.ProvisionConfig{
			Mode:                  mode,
			VMName:                c.resolved.Name,
			VMID:                  c.vmID,
			VMDir:                 c.vmDir,
			CloudInitDir:          filepath.Join(c.vmDir, "cloud-init"),
			GuestIP:               c.guestIP,
			TapName:               c.tapName,
			User:                  c.resolved.User,
			IPv4Gateway:           c.resolved.Network.IPv4Gateway,
			NetworkPrefixLen:      c.resolved.NetworkPrefixLen,
			SkipNetworkConfig:     c.resolved.SkipCINetworkConfig,
			SSHPubkeys:            pubkeys,
			CustomUserDataPath:    c.resolved.CustomUserDataPath,
			NocloudNetPort:        c.resolved.NocloudNetPort,
			CloudInitISOPath:      c.resolved.CloudInitISOPath,
			KeepCloudInitISO:      c.resolved.KeepCloudInitISO,
			CloudInitISOName:      c.resolved.CloudInitISOName,
			NocloudPortRangeStart: c.resolved.NocloudPortRangeStart,
			NocloudPortRangeEnd:   c.resolved.NocloudPortRangeEnd,
			NocloudMaxPortRetries: c.resolved.NocloudMaxPortRetries,
		}
		ciProvisioner := cloudinit.NewProvisioner(ciConfig, nil)
		ciResult, ciErr := ciProvisioner.Provision(ctx)
		if ciErr != nil {
			return fmt.Errorf("cloud-init provisioning failed: %w", ciErr)
		}
		c.cloudInitResult = &cloudInitResult{
			mode:        ciResult.Mode,
			isoPath:     ciResult.ISOPath,
			nocloudURL:  ciResult.NocloudURL,
			nocloudPort: &ciResult.NocloudPort,
			nocloudPID:  ciResult.NocloudPID,
		}

		if mode == model.CloudInitModeISO {
			c.markCreated("cloud-init-iso")
		} else {
			c.markCreated("cloud-init-net")
		}
	}

	// Deblob (OS cache cleanup) unless explicitly skipped
	if !c.resolved.SkipDeblob {
		// Pass the image's pre-detected distro to avoid redundant OS detection.
		// Matches Python: provisioner.deblob(os_type=self.resolved.image.distro)
		var distro string
		if c.resolved.Image.Distro != nil {
			distro = *c.resolved.Image.Distro
		}
		provisioner.Deblob(distro)
	}

	// Fix fstab for Firecracker (superfloppy /dev/vda layout)
	provisioner.FixFstab()

	// Execute all queued provisioning operations
	provisioner.Run()

	// Progress: firecracker
	if c.onProgress != nil {
		c.onProgress(errs.ProgressEvent{Phase: "firecracker", Status: "running", Message: "Starting Firecracker microVM..."})
	}

	// --- Firecracker config ---
	fcConfig := c.buildFirecrackerConfig()
	if fcConfig == nil {
		return fmt.Errorf("Firecracker config is not set in context")
	}

	spawner := vm.NewFirecrackerSpawner(fcConfig)
	c.fcManager = fcConfig
	spawner.WriteToFile()
	c.markCreated("firecracker")

	// Validate socket path won't exceed Unix domain socket limit
	socketPath := spawner.APISocketPath()
	if len(socketPath) >= 108 {
		return fmt.Errorf("VM ID '%s' produces a socket path that is too long (%d chars, max 107). This is a system limit for Unix domain sockets. Path: %s",
			c.vmID, len(socketPath), socketPath)
	}

	// Console relay setup (before spawn)
	if c.resolved.EnableConsole {
		consoleCtrl := console.NewController(c.vmID, c.vmDir, c.name,
			c.resolved.ConsolePIDFilename, c.resolved.ConsoleSocketFilename, "firecracker.console.log")
		ptyFD, ptyErr := consoleCtrl.CreatePTY()
		if ptyErr != nil {
			return fmt.Errorf("console PTY creation failed: %w", ptyErr)
		}
		c.relay = consoleCtrl
		fcConfig.RelayEnabled = true
		fcConfig.RelayClientFD = &ptyFD
	}

	// Spawn Firecracker
	if err := spawner.Spawn(); err != nil {
		return fmt.Errorf("failed to spawn Firecracker: %w", err)
	}

	// Store spawner for toModel() (matches Python's ctx.fc_manager = spawner)
	c.spawner = spawner

	// Start console relay after spawn (matches Python's relay.close_client_fd(); relay.start())
	if c.resolved.EnableConsole && c.relay != nil {
		c.relay.CloseClientFD()
		_, _, startErr := c.relay.Start(ctx)
		if startErr != nil {
			return fmt.Errorf("console relay start failed: %w", startErr)
		}
		c.markCreated("console_relay")
	}

	if c.onProgress != nil {
		c.onProgress(errs.ProgressEvent{Phase: "complete", Status: "complete", Message: "VM created successfully"})
	}

	return nil
}

// consoleRelayRef is a simplified console relay reference for the Go port.
type consoleRelayRef struct {
	vmID  string
	vmDir string
}

func (c *vmCreateContext) cloneImage() error {
	if c.resolved == nil {
		return fmt.Errorf("Failed to resolve necessary dependencies")
	}
	fsType := c.resolved.Image.FSType
	if fsType == "" {
		fsType = "ext4"
	}
	vmRootfsPath := filepath.Join(c.vmDir, "rootfs."+fsType)

	imageSvc := image.NewService(image.NewRepository(nil), c.cacheDir)
	if _, err := imageSvc.EnsureCached([]*model.ImageItem{c.resolved.Image}); err != nil {
		return fmt.Errorf("ensure cached image: %w", err)
	}
	if err := imageSvc.MaterializeTo(c.resolved.Image.ID, fsType, vmRootfsPath); err != nil {
		return fmt.Errorf("materialize image: %w", err)
	}

	c.rootfsPath = vmRootfsPath
	return nil
}

func (c *vmCreateContext) markCreated(resource string) {
	c.resourcesCreated[resource] = true
}

func (c *vmCreateContext) wasCreated(resource string) bool {
	return c.resourcesCreated[resource]
}

func (c *vmCreateContext) cleanup() {
	if c.vmDir == "" || c.resolved == nil {
		return
	}

	// Cloud-init: stop nocloud server and remove firewall rules
	if c.wasCreated("cloud-init-net") && c.cloudInitResult != nil && c.cloudInitResult.nocloudNetManager != nil {
		if mgr, ok := c.cloudInitResult.nocloudNetManager.(interface{ Stop() error }); ok {
			_ = mgr.Stop()
		}
		// Remove all nocloud-net firewall rules
		for _, rule := range c.cloudInitResult.nocloudNetRules {
			if fwRule, ok := rule.(interface{ Remove() error }); ok {
				_ = fwRule.Remove()
			}
		}
	}

	// Networking: remove TAP device
	if c.wasCreated("network_tap") && c.tapName != "" && c.resolved.Network != nil {
		netSvc := network.NewService(network.NewRepository(nil), nil)
		_ = netSvc.RemoveTap(context.Background(), c.tapName, c.resolved.Network.Bridge, c.resolved.Network.ID)

		// Release IP lease
		leaseRepo := network.NewLeaseRepository(nil)
		_ = leaseRepo.ReleaseByVM(context.Background(), c.vmID)
	}

	// Console relay: stop relay process
	if c.wasCreated("console_relay") && c.relay != nil {
		c.relay.Cleanup()
	}

	// Firecracker: stop firecracker process
	if c.wasCreated("firecracker") && c.fcManager != nil {
		spawner := vm.NewFirecrackerSpawner(c.fcManager)
		spawner.Cleanup()
	}

	// VM directory: remove all created files
	if c.wasCreated("vm_dir") && c.vmDir != "" {
		os.RemoveAll(c.vmDir)
	}
}

// ── Respawning (for_respawn / respawn_execute) ──
// Matches Python's VMCreateContext.for_respawn() and respawn_execute().

// forRespawn creates a VMCreateContext for respawning a stopped VM from its stored state.
// Matches Python's VMCreateContext.for_respawn() exactly.
func (c *vmCreateContext) forRespawn(vm *model.VM, snapshotMode bool) {
	c.name = vm.Name
	c.vmID = vm.ID
	c.vmDir = filepath.Join(c.cacheDir, "vms", vm.ID)
	c._vm = vm
	c._snapshotMode = snapshotMode
	c.guestIP = vm.IPv4
	c.guestMAC = vm.MAC
	c.tapName = vm.TapDevice

	// Build rootfs path from VM state
	rootfsSuffix := vm.RootfsSuffix
	if rootfsSuffix == "" {
		rootfsSuffix = "ext4"
	}
	c.rootfsPath = filepath.Join(c.vmDir, "rootfs."+rootfsSuffix)

	// Build resolved from VM state — makes buildFirecrackerConfig() work unchanged
	c.resolved = &resolvedVMCreateInput{
		Name:                  vm.Name,
		VMID:                  vm.ID,
		VMDir:                 c.vmDir,
		VCPUCount:             vm.VCPUCount,
		MemSizeMiB:            vm.MemSizeMiB,
		User:                  safeDeref(vm.SSHUser),
		DNSServer:             "1.1.1.1",
		GuestMACPrefix:        "02:FC",
		NetworkPrefixLen:      24,
		NetworkNetmask:        "255.255.255.0",
		CloudInitMode:         model.CloudInitModeOFF,
		PCIEnabled:            vm.PCIEnabled,
		NestedVirt:            vm.NestedVirt,
		EnableConsole:         vm.EnableConsole,
		EnableLogging:         vm.EnableLogging,
		EnableMetrics:         vm.EnableMetrics,
		LogLevel:              "Info",
		LogFilename:           "firecracker.log",
		SerialOutputFilename:  "serial.out",
		MetricsFilename:       "metrics.log",
		APISocketFilename:     "api.socket",
		PIDFilename:           "firecracker.pid",
		ConfigFilename:        "firecracker.json",
		ConsoleSocketFilename: "console.sock",
		ConsolePIDFilename:    "console.pid",
		CloudInitISOName:      "seed.iso",
		BootArgs:              safeDeref(vm.BootArgs),
		LSMFlags:              safeDeref(vm.LSMFlags),
	}

	// Set cloud_init_mode from VM state
	if vm.CloudInitMode != "" {
		switch vm.CloudInitMode {
		case "inject":
			c.resolved.CloudInitMode = model.CloudInitModeINJECT
		case "net":
			c.resolved.CloudInitMode = model.CloudInitModeNET
		case "iso":
			c.resolved.CloudInitMode = model.CloudInitModeISO
		}
	}

	// Fabricate cloud_init_result for buildFirecrackerConfig() (matches Python's fabrication)
	ciMode := model.CloudInitModeOFF
	if vm.CloudInitMode != "" {
		switch vm.CloudInitMode {
		case "inject":
			ciMode = model.CloudInitModeINJECT
		case "net":
			ciMode = model.CloudInitModeNET
		case "iso":
			ciMode = model.CloudInitModeISO
		}
	}

	isoPath := filepath.Join(c.vmDir, "cloud-init", "seed.iso")
	var isoPathPtr *string
	if _, err := os.Stat(isoPath); err == nil {
		isoPathPtr = strPtr(isoPath)
	}

	var nocloudURL *string
	if vm.NocloudNetPort != nil && vm.IPv4 != "" {
		url := fmt.Sprintf("http://%s:%d/", vm.IPv4, *vm.NocloudNetPort)
		nocloudURL = &url
	}

	c.cloudInitResult = &cloudInitResult{
		mode:       ciMode,
		isoPath:    isoPathPtr,
		nocloudURL: nocloudURL,
	}
}

// respawnExecute executes the respawn flow for a stopped VM.
// Matches Python's VMCreateContext.respawn_execute() exactly.
// Handles: nocloud-net restart, force-kill old process, TAP re-ensure,
// then delegates to buildFirecrackerConfig + spawn.
// DB updates (pid, status) are handled by the caller.
func (c *vmCreateContext) respawnExecute(ctx context.Context) error {
	if c._vm == nil {
		return fmt.Errorf("VM not set on VMCreateContext for respawn")
	}

	if c._vm.Network == nil {
		return fmt.Errorf("Network not found for VM '%s' (ID: %s)", c._vm.Name, c._vm.NetworkID)
	}

	// Resolve network if not enriched (should not happen, but be safe)
	netItem := c._vm.Network
	if netItem == nil {
		netRepo := network.NewRepository(nil)
		netItem, _ = netRepo.Get(ctx, c._vm.NetworkID)
		if netItem == nil {
			return fmt.Errorf("Network not found for VM '%s' (ID: %s)", c._vm.Name, c._vm.NetworkID)
		}
		// Re-set in c._vm so subsequent code can use it
		c._vm.Network = netItem
	}

	cloudInitMode := model.CloudInitModeOFF
	if c.cloudInitResult != nil {
		cloudInitMode = c.cloudInitResult.mode
	}

	// ── Restart nocloud-net server if needed ──
	if cloudInitMode == model.CloudInitModeNET {
		port := 0
		if c._vm.NocloudNetPort != nil {
			port = *c._vm.NocloudNetPort
		}
		if port == 0 || (c._vm.NocloudNetPID != nil && !isProcessRunningGo(*c._vm.NocloudNetPID)) {
			nocloudSvc := nocloudnet.NewNoCloudServer(c._vm.ID, c._vm.Name, c.vmDir, netItem.IPv4Gateway, port, 8000, 9000, 100)
			if _, newPort, _, startErr := nocloudSvc.Start(ctx, c.vmDir); startErr != nil {
				slog.Warn("Failed to start/restart nocloud-net server", "vm", c._vm.Name, "error", startErr)
			} else if port == 0 {
				port = newPort
			}
		}
	}

	// ── Force-kill any remaining Firecracker process ──
	if c._vm.PID > 0 && isProcessRunningGo(c._vm.PID) {
		proc, err := os.FindProcess(c._vm.PID)
		if err == nil {
			// Try SIGTERM first, then SIGKILL (matches Python's kill_and_wait)
			_ = proc.Signal(syscall.SIGTERM)
			time.Sleep(100 * time.Millisecond)
			if isProcessRunningGo(c._vm.PID) {
				_ = proc.Kill()
				time.Sleep(50 * time.Millisecond)
			}
		}
	}

	// ── Re-ensure TAP device exists before spawning ──
	netSvc := network.NewService(network.NewRepository(nil), nil)
	bridgeAddr, calcErr := network.ComputeBridgeAddress(netItem.IPv4Gateway, netItem.Subnet)
	if calcErr != nil {
		return fmt.Errorf("compute bridge address: %w", calcErr)
	}
	_ = netSvc.EnsureBridge(ctx, netItem.Bridge, bridgeAddr)
	_ = netSvc.EnsureTap(ctx, c.tapName, netItem.Bridge, netItem.ID, netItem.Subnet)
	netSvc.FlushARP(netItem.Bridge)

	// ── Build config and spawn ──
	fcConfig := c.buildFirecrackerConfig()
	if fcConfig == nil {
		return fmt.Errorf("Firecracker config is not set in context")
	}
	fcConfig.SnapshotMode = c._snapshotMode

	// Console relay setup (before spawn)
	if c._vm.EnableConsole {
		consoleCtrl := console.NewController(c.vmID, c.vmDir, c.name,
			"console.pid", "console.sock", "firecracker.console.log")
		ptyFD, ptyErr := consoleCtrl.CreatePTY()
		if ptyErr != nil {
			return fmt.Errorf("console PTY creation failed: %w", ptyErr)
		}
		c.relay = consoleCtrl
		fcConfig.RelayEnabled = true
		fcConfig.RelayClientFD = &ptyFD
	}

	spawner := vm.NewFirecrackerSpawner(fcConfig)
	c.fcManager = fcConfig
	spawner.WriteToFile()

	if err := spawner.Spawn(); err != nil {
		return fmt.Errorf("failed to spawn Firecracker: %w", err)
	}

	// Start console relay after spawn
	if c._vm.EnableConsole && c.relay != nil {
		c.markCreated("console_relay")
	}

	return nil
}

// buildFirecrackerConfig builds a FirecrackerConfig from the resolved create context.
// Matches Python's VMCreateContext.build_firecracker_config() exactly.
func (c *vmCreateContext) buildFirecrackerConfig() *model.FirecrackerConfig {
	if c.resolved == nil {
		return nil
	}

	var cpuVendor *string
	var cpuArchitecture *string
	if c.db != nil {
		ctx := context.Background()
		hostRepo := host.NewRepository(c.db)
		if hostState, err := hostRepo.GetState(ctx); err == nil && hostState != nil {
			cpuVendor = hostState.CPUVendor
			cpuArchitecture = hostState.CPUArchitecture
		}
	}

	ciMode := model.CloudInitModeOFF
	if c.cloudInitResult != nil {
		switch c.cloudInitResult.mode {
		case model.CloudInitModeINJECT:
			ciMode = model.CloudInitModeINJECT
		case model.CloudInitModeNET:
			ciMode = model.CloudInitModeNET
		case model.CloudInitModeISO:
			ciMode = model.CloudInitModeISO
		}
	}

	fcConfig := &model.FirecrackerConfig{
		VMDir:                c.vmDir,
		RootfsPath:           c.rootfsPath,
		BinaryPath:           c.resolved.Binary.Path,
		KernelPath:           c.resolved.Kernel.Path,
		VCPUCount:            c.resolved.VCPUCount,
		MemSizeMiB:           c.resolved.MemSizeMiB,
		GuestIP:              c.guestIP,
		GuestMAC:             c.guestMAC,
		TapName:              c.tapName,
		NetworkGateway:       c.resolved.Network.IPv4Gateway,
		NetworkNetmask:       c.resolved.NetworkNetmask,
		ImageFSUUID:          c.resolved.Image.FSUUID,
		ImageFSType:          c.resolved.Image.FSType,
		BootArgs:             strPtr(c.resolved.BootArgs),
		LSMFlags:             strPtr(c.resolved.LSMFlags),
		PCIEnabled:           c.resolved.PCIEnabled,
		NestedVirt:           c.resolved.NestedVirt,
		CPUVendor:            cpuVendor,
		CPUArchitecture:      cpuArchitecture,
		CloudInitMode:        &ciMode,
		EnableConsole:        c.resolved.EnableConsole,
		EnableLogging:        c.resolved.EnableLogging,
		EnableMetrics:        c.resolved.EnableMetrics,
		LogLevel:             c.resolved.LogLevel,
		LogFilename:          c.resolved.LogFilename,
		SerialOutputFilename: c.resolved.SerialOutputFilename,
		MetricsFilename:      c.resolved.MetricsFilename,
		APISocketFilename:    c.resolved.APISocketFilename,
		PIDFilename:          c.resolved.PIDFilename,
		ConfigFilename:       c.resolved.ConfigFilename,
		ExtraDrives:          c.resolved.ExtraDrives,
	}

	// Cloud-init info from result (isoPath, nocloudURL)
	if c.cloudInitResult != nil {
		if c.cloudInitResult.isoPath != nil {
			fcConfig.CloudInitISOPath = strPtr(*c.cloudInitResult.isoPath)
		}
		if c.cloudInitResult.nocloudURL != nil {
			fcConfig.CloudInitNoCloudURL = strPtr(*c.cloudInitResult.nocloudURL)
		}
	}

	// CPU config
	if c.resolved.CPUConfig != nil {
		fcConfig.CPUConfig = c.resolved.CPUConfig
	}

	return fcConfig
}

func (c *vmCreateContext) toModel() *model.VM {
	if c.resolved == nil {
		return nil
	}

	// Python's to_model() requires self.fc_manager and self.fc_manager.pid
	if c.spawner == nil {
		return nil
	}
	if c.spawner.PID() == nil {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)

	vm := &model.VM{
		ID:               c.vmID,
		Name:             c.resolved.Name,
		PID:              safeDerefInt(c.spawner.PID()),
		ExitCode:         nil,
		ProcessStartTime: c.spawner.ProcessStartTime(),
		Status:           model.StatusRunning,
		IPv4:             c.guestIP,
		MAC:              c.guestMAC,
		NetworkID:        c.resolved.Network.ID,
		TapDevice:        c.tapName,
		ImageID:          c.resolved.Image.ID,
		KernelID:         c.resolved.Kernel.ID,
		BinaryID:         c.resolved.Binary.ID,
		VCPUCount:        c.resolved.VCPUCount,
		MemSizeMiB:       c.resolved.MemSizeMiB,
		DiskSizeMiB:      c.resolved.DiskSizeMiB,
		APISocketPath:    filepath.Join(c.vmDir, c.resolved.APISocketFilename),
		ConfigPath:       filepath.Join(c.vmDir, c.resolved.ConfigFilename),
		CloudInitMode:    string(c.resolved.CloudInitMode),
		RootfsPath:       c.rootfsPath,
		RootfsSuffix:     c.resolved.Image.FSType,
		PCIEnabled:       c.resolved.PCIEnabled,
		NestedVirt:       c.resolved.NestedVirt,
		EnableLogging:    c.resolved.EnableLogging,
		EnableMetrics:    c.resolved.EnableMetrics,
		EnableConsole:    c.resolved.EnableConsole,
		CreatedAt:        now,
		UpdatedAt:        now,
		SSHKeys:          c.resolved.SSHKeys,
		SSHUser:          &c.resolved.User,
		VolumeIDs:        []string{},
	}

	// Set cpu_config from resolved input (matches Python: if self.resolved.cpu_config is not None)
	if c.resolved.CPUConfig != nil {
		vm.CPUConfig = c.resolved.CPUConfig
	}

	vm.LogPath = strPtr(filepath.Join(c.vmDir, c.resolved.LogFilename))
	vm.SerialOutputPath = strPtr(filepath.Join(c.vmDir, c.resolved.SerialOutputFilename))

	if c.resolved.BootArgs != "" {
		vm.BootArgs = &c.resolved.BootArgs
	}
	if c.resolved.LSMFlags != "" {
		vm.LSMFlags = &c.resolved.LSMFlags
	}

	// Python: nocloud_net_port and nocloud_net_pid from cloud_init_result
	if c.cloudInitResult != nil && c.cloudInitResult.nocloudNetManager != nil {
		if c.cloudInitResult.nocloudPort != nil {
			vm.NocloudNetPort = c.cloudInitResult.nocloudPort
		}
		if c.cloudInitResult.nocloudPID != nil {
			vm.NocloudNetPID = c.cloudInitResult.nocloudPID
		}
	} else if c.resolved.NocloudNetPort != nil {
		vm.NocloudNetPort = c.resolved.NocloudNetPort
	}

	// Set relay PID and socket path (matches Python's to_model relay block)
	if c.relay != nil {
		if p := c.relay.GetPID(); p != nil {
			vm.RelayPID = p
		}
		if s := c.relay.SocketPath(); s != "" {
			vm.RelaySocketPath = &s
		}
	}

	return vm
}

func (o *VMOperation) buildResolvedInput(ctx context.Context, input *inputs.VMCreateInput, vmID, vmDir string) (*resolvedVMCreateInput, error) {
	imageRepo := image.NewRepository(o.db)
	kernelRepo := kernel.NewRepository(o.db)
	netRepo := network.NewRepository(o.db)
	keyRepo := key.NewRepository(o.db)

	// Resolve image (handles selectors like "alpine:3.21" and ID prefixes)
	var image *model.ImageItem
	var err error
	if input.Image != nil {
		selector := *input.Image
		// Try exact name first
		image, err = imageRepo.GetByName(ctx, selector)
		if err == nil && image == nil {
			// Try type:version selector (e.g. "alpine:3.21")
			if parts := strings.SplitN(selector, ":", 2); len(parts) == 2 {
				image, err = imageRepo.GetByVersionAndType(ctx, parts[1], parts[0])
			}
		}
		if err == nil && image == nil {
			// Try as ID prefix
			image, err = imageRepo.Get(ctx, selector)
		}
	} else {
		image, err = imageRepo.GetDefault(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve image: %w", err)
	}
	if image == nil {
		if input.Image != nil {
			return nil, fmt.Errorf("resolve image: %s is not present locally", *input.Image)
		}
		return nil, fmt.Errorf("resolve image: no default image set")
	}

	// Resolve kernel
	var kernel *model.KernelItem
	if input.KernelID != nil {
		kernel, err = kernelRepo.Get(ctx, *input.KernelID)
		if err != nil || kernel == nil {
			kernel, err = kernelRepo.GetByName(ctx, *input.KernelID)
		}
	} else {
		kernel, err = kernelRepo.GetDefault(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve kernel: %w", err)
	}
	if kernel == nil {
		if input.KernelID != nil {
			return nil, fmt.Errorf("resolve kernel: %s is not present locally", *input.KernelID)
		}
		return nil, fmt.Errorf("resolve kernel: no default kernel set")
	}

	// Resolve network
	var network *model.Network
	if input.NetworkName != nil {
		network, err = netRepo.GetByName(ctx, *input.NetworkName)
		if err != nil || network == nil {
			network, err = netRepo.Get(ctx, *input.NetworkName)
		}
	} else {
		network, err = netRepo.GetDefault(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve network: %w", err)
	}
	if network == nil {
		return nil, fmt.Errorf("resolve network: no default network")
	}

	// Resolve binary from DB (matches Python's Repository resolution)
	binaryRepo := binary.NewRepository(o.db)
	binary := o.resolveBinary(ctx, input.BinaryID, input.FirecrackerBin, binaryRepo)

	// Resolve SSH keys
	sshKeyNames := input.SSHKeys
	if len(sshKeyNames) == 0 {
		defaultKeys, _ := keyRepo.GetDefaults(ctx)
		for _, k := range defaultKeys {
			sshKeyNames = append(sshKeyNames, k.Name)
		}
	}

	// Network details
	networkPrefixLen := 24
	networkNetmask := "255.255.255.0"

	// Defaults (matching constants.py: vcpu_count=1, mem_size_mib=512)
	vcpuCount := 1
	if input.VCPUCount != nil {
		vcpuCount = *input.VCPUCount
	}

	memSizeMiB := 0
	if input.MemSizeMib != nil && *input.MemSizeMib != "" {
		if bytes, err := infra.ParseDiskSizeToBytes(*input.MemSizeMib); err == nil {
			memSizeMiB = int(bytes / (1024 * 1024))
		}
	}
	if memSizeMiB <= 0 {
		memSizeMiB = 512
	}

	diskSizeMiB := image.MinRootfsSizeMiB
	diskSizeBytes := int64(diskSizeMiB) * 1024 * 1024
	if input.DiskSize != nil {
		if bytes, err := infra.ParseDiskSizeToBytes(*input.DiskSize); err == nil {
			diskSizeBytes = bytes
			diskSizeMiB = int(bytes / (1024 * 1024))
		}
	}

	user := "root"
	if input.User != nil {
		user = *input.User
	}

	ciMode := model.CloudInitModeOFF
	if input.CloudInitMode != nil {
		switch *input.CloudInitMode {
		case "inject":
			ciMode = model.CloudInitModeINJECT
		case "net":
			ciMode = model.CloudInitModeNET
		case "iso":
			ciMode = model.CloudInitModeISO
		}
	}

	nestedVirt := false
	if input.NestedVirt != nil {
		nestedVirt = *input.NestedVirt
	}
	pciEnabled := false
	if input.PCIEnabled != nil {
		pciEnabled = *input.PCIEnabled
	}
	if nestedVirt {
		pciEnabled = true
	}

	enableConsole := !input.NoConsole

	// Resolve boot_args and lsm_flags from input (matches Python's VMCreateRequest.resolve())
	bootArgs := ""
	if input.BootArgs != nil {
		bootArgs = *input.BootArgs
	}
	lsmFlags := ""
	if input.LSMFlags != nil {
		lsmFlags = *input.LSMFlags
	}

	// Resolve enable_logging and enable_metrics from input (matches Python)
	enableLogging := false
	if input.EnableLogging != nil {
		enableLogging = *input.EnableLogging
	}
	enableMetrics := false
	if input.EnableMetrics != nil {
		enableMetrics = *input.EnableMetrics
	}

	// Resolve provisioner type from settings (matches Python)
	provisionerType := provisioner.ProvisionerLoopMount
	if o.db != nil {
		row := o.db.QueryRowContext(ctx, "SELECT value FROM user_settings WHERE category = 'settings' AND key = 'guestfs_enabled'")
		var val string
		if err := row.Scan(&val); err == nil {
			if val == "true" || val == "1" {
				provisionerType = provisioner.ProvisionerGuestFS
			}
		}
	}

	// Extract SSH key IDs from the key items (not names, matching Python's to_model())
	sshKeyIDs := make([]string, len(sshKeyNames))
	for i, name := range sshKeyNames {
		key, err := keyRepo.GetByName(ctx, name)
		if err == nil && key != nil {
			sshKeyIDs[i] = key.ID
		} else {
			// Fall back to using the name
			sshKeyIDs[i] = name
		}
	}

	return &resolvedVMCreateInput{
		Name:                  input.Name,
		VMID:                  vmID,
		VMDir:                 vmDir,
		VCPUCount:             vcpuCount,
		MemSizeMiB:            memSizeMiB,
		User:                  user,
		DNSServer:             "1.1.1.1",
		GuestMACPrefix:        "02:FC",
		Network:               network,
		Image:                 image,
		Kernel:                kernel,
		Binary:                binary,
		NetworkPrefixLen:      networkPrefixLen,
		CloudInitMode:         ciMode,
		PCIEnabled:            pciEnabled,
		NestedVirt:            nestedVirt,
		EnableConsole:         enableConsole,
		EnableLogging:         enableLogging,
		EnableMetrics:         enableMetrics,
		SkipCleanup:           input.SkipCleanup,
		SkipDeblob:            input.SkipDeblob,
		NetworkNetmask:        networkNetmask,
		DiskSizeBytes:         diskSizeBytes,
		DiskSizeMiB:           diskSizeMiB,
		BootArgs:              bootArgs,
		LSMFlags:              lsmFlags,
		LogLevel:              "Info",
		LogFilename:           "firecracker.log",
		SerialOutputFilename:  "serial.out",
		MetricsFilename:       "metrics.log",
		APISocketFilename:     "api.socket",
		PIDFilename:           "firecracker.pid",
		ConfigFilename:        "firecracker.json",
		ConsoleSocketFilename: "console.sock",
		ConsolePIDFilename:    "console.pid",
		CloudInitISOName:      "seed.iso",
		NocloudPortRangeStart: 8000,
		NocloudPortRangeEnd:   9000,
		NocloudMaxPortRetries: 100,
		RequestedGuestIP:      input.RequestedGuestIP,
		RequestedGuestMAC:     input.RequestedGuestMAC,
		CPUConfig:             mapToVMCPUCfg(input.CPUConfig),
		SSHKeys:               sshKeyIDs,
		Provisioner:           model.ProvisionerType(provisionerType),
	}, nil
}

// resolveBinary resolves the firecracker binary from DB or falls back to hardcoded path.
// Matches Python's VMCreateRequest binary resolution: tries explicit ID/path, then default, then fallback.
func (o *VMOperation) resolveBinary(ctx context.Context, binaryID, firecrackerBin *string, binaryRepo binary.Repository) *model.BinaryItem {
	// 1. Explicit binary ID from input
	if binaryID != nil && *binaryID != "" {
		bin, err := binaryRepo.Get(ctx, *binaryID)
		if err == nil && bin != nil {
			return bin
		}
	}
	// 2. Explicit path from --firecracker-bin
	if firecrackerBin != nil && *firecrackerBin != "" {
		binaries, err := binaryRepo.ListByName(ctx, "firecracker")
		if err == nil {
			for _, b := range binaries {
				if b.Path == *firecrackerBin {
					return b
				}
			}
		}
		// Create a minimal binary item for the explicit path
		return &model.BinaryItem{Name: "firecracker", Path: *firecrackerBin}
	}
	// 3. Default firecracker from DB
	bin, err := binaryRepo.GetDefault(ctx, "firecracker")
	if err == nil && bin != nil {
		return bin
	}
	// 4. Fallback to hardcoded path
	return &model.BinaryItem{Name: "firecracker", Path: "/usr/local/bin/firecracker"}
}

// mapToVMCPUCfg converts a map[string]any CPU config to *model.CpuConfig.
func mapToVMCPUCfg(m map[string]interface{}) *model.CpuConfig {
	if m == nil {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var cfg model.CpuConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

func (o *VMOperation) resolveSingleVM(ctx context.Context, ident string) (*model.VM, error) {
	if ident == "" {
		return nil, fmt.Errorf("VM identifier is required")
	}
	// Match Python's VMResolver.resolve() order:
	// 1. Try by name first
	vm, err := o.vmRepo.GetByName(ctx, ident)
	if err == nil && vm != nil {
		return vm, nil
	}
	// 2. If contains '.', try by IP (matches Python's by_ip)
	if strings.Contains(ident, ".") {
		vm, err = o.vmRepo.FindByIP(ctx, ident)
		if err == nil && vm != nil {
			return vm, nil
		}
	}
	// 3. If contains ':', try by MAC (matches Python's by_mac)
	if strings.Contains(ident, ":") {
		vm, err = o.vmRepo.FindByMAC(ctx, ident)
		if err == nil && vm != nil {
			return vm, nil
		}
	}
	// 4. Try by ID prefix (matches Python's find_by_prefix)
	matches, err := o.vmRepo.FindByPrefix(ctx, ident)
	if err == nil {
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			var names []string
			for _, m := range matches {
				names = append(names, m.Name)
			}
			return nil, fmt.Errorf("ID %s matches multiple VMs: %s", ident, strings.Join(names, ", "))
		}
	}
	return nil, fmt.Errorf("VM not found: %s", ident)
}

func (o *VMOperation) generateBatchNames(baseName string, count int) []string {
	names := make([]string, count)
	for i := 0; i < count; i++ {
		names[i] = fmt.Sprintf("%s-%d", baseName, i+1)
	}
	return names
}

// ── Standalone helper functions ──

func isProcessRunningGo(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func VMGenerateMAC(prefix string) string {
	if prefix == "" {
		prefix = "02:FC"
	}
	return prefix + fmt.Sprintf(":%02x:%02x:%02x",
		time.Now().UnixNano()&0xff,
		os.Getpid()&0xff,
		time.Now().UnixNano()>>8&0xff)
}

func VMGenerateTAPName(netName, vmName string) string {
	h := sha256.Sum256([]byte(netName + ":" + vmName))
	return fmt.Sprintf("tap-%x", h[:4])
}

func strPtr(s string) *string { return &s }

// isFirecrackerVersionAtLeast checks if a Firecracker version string is >= minVersion.
// Matches Python's VersionGate.require() logic.
func isFirecrackerVersionAtLeast(ver, minVersion string) bool {
	vParts, _ := version.SplitVersionParts(ver)
	mParts, _ := version.SplitVersionParts(minVersion)
	for i := 0; i < len(vParts) && i < len(mParts); i++ {
		if vParts[i] != mParts[i] {
			return vParts[i] > mParts[i]
		}
	}
	return len(vParts) >= len(mParts)
}

func intPtr(i int) *int { return &i }

func safeDeref(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}

func safeDerefInt(i *int) int {
	if i != nil {
		return *i
	}
	return 0
}

// resolvedFromBuilderOutput converts a VMCreateResolved (from VMCreateBuilder.Build)
// to the internal resolvedVMCreateInput used by executeCreate.
// This bridges the public API layer to the internal VM creation pipeline.
func resolvedFromBuilderOutput(r *inputs.VMCreateResolved) *resolvedVMCreateInput {
	if r == nil {
		return nil
	}

	// Fields are already typed — no type assertions needed

	// Convert SSHKeys: []*key.SSHKeyItem -> []string (names)
	var sshKeyNames []string
	for _, keyItem := range r.SSHKeys {
		if keyItem != nil {
			sshKeyNames = append(sshKeyNames, keyItem.Name)
		}
	}

	// BootArgs: *string -> string
	bootArgs := ""
	if r.BootArgs != nil {
		bootArgs = *r.BootArgs
	}

	return &resolvedVMCreateInput{
		Name:                  r.Name,
		VMID:                  r.VMID,
		VMDir:                 r.VMDir,
		VCPUCount:             r.VCPUCount,
		MemSizeMiB:            r.MemSizeMib,
		User:                  r.User,
		DNSServer:             r.DNSServer,
		RootUID:               r.RootUID,
		RootGID:               r.RootGID,
		UserUID:               r.UserUID,
		UserGID:               r.UserGID,
		GuestMACPrefix:        r.GuestMACPrefix,
		Network:               r.Network,
		Image:                 r.Image,
		Kernel:                r.Kernel,
		Binary:                r.Binary,
		NetworkPrefixLen:      r.NetworkPrefixLen,
		CloudInitMode:         r.CloudInitMode,
		SkipCINetworkConfig:   r.SkipCINetworkConfig,
		PCIEnabled:            r.PCIEnabled,
		NestedVirt:            r.NestedVirt,
		EnableConsole:         r.EnableConsole,
		EnableLogging:         r.EnableLogging,
		EnableMetrics:         r.EnableMetrics,
		KeepCloudInitISO:      r.KeepCloudInitISO,
		SkipCleanup:           r.SkipCleanup,
		SkipDeblob:            r.SkipDeblob,
		NetworkNetmask:        r.NetworkNetmask,
		DiskSizeBytes:         r.DiskSizeBytes,
		DiskSizeMiB:           r.DiskSizeMib,
		LSMFlags:              r.LSMFlags,
		LogLevel:              r.LogLevel,
		LogFilename:           r.LogFilename,
		SerialOutputFilename:  r.SerialOutputFilename,
		MetricsFilename:       r.MetricsFilename,
		APISocketFilename:     r.APISocketFilename,
		PIDFilename:           r.PIDFilename,
		ConfigFilename:        r.ConfigFilename,
		ConsoleSocketFilename: r.ConsoleSocketFilename,
		ConsolePIDFilename:    r.ConsolePIDFilename,
		CloudInitISOName:      r.CloudInitISOName,
		NocloudPortRangeStart: r.NocloudPortRangeStart,
		NocloudPortRangeEnd:   r.NocloudPortRangeEnd,
		NocloudMaxPortRetries: r.NocloudMaxPortRetries,
		RequestedGuestIP:      r.RequestedGuestIP,
		RequestedGuestMAC:     r.RequestedGuestMAC,
		NocloudNetPort:        r.NocloudNetPort,
		CustomUserDataPath:    r.CustomUserDataPath,
		CloudInitISOPath:      r.CloudInitISOPath,
		CPUConfig:             r.CPUConfig,
		BootArgs:              bootArgs,
		SSHKeys:               sshKeyNames,
		Provisioner:           model.ProvisionerType(r.Provisioner),
		Volumes:               r.Volumes,
		ExtraDrives:           convertExtraDrives(r.ExtraDrives),
	}
}

// convertExtraDrives converts from public inputs.DriveConfig to []model.DriveConfig.
func convertExtraDrives(drives []inputs.DriveConfig) []model.DriveConfig {
	result := make([]model.DriveConfig, len(drives))
	for i, d := range drives {
		result[i] = model.DriveConfig{
			DriveID:      d.DriveID,
			PathOnHost:   d.PathOnHost,
			IsRootDevice: d.IsRootDevice,
			IsReadOnly:   d.IsReadOnly,
		}
	}
	return result
}

// setupConsoleRelay creates a Controller and PTY for the given VM.
// Matches Python's ConsoleOperation startup sequence.
func (o *VMOperation) setupConsoleRelay(ctx context.Context, vm *model.VM) (*console.Controller, error) {
	relayPath := filepath.Join(o.cacheDir, "vms", vm.ID)
	cc := console.NewController(vm.ID, relayPath, vm.Name, "console.pid", "console.sock", "firecracker.console.log")
	ptyFD, err := cc.CreatePTY()
	if err != nil {
		return nil, fmt.Errorf("create PTY for console relay: %w", err)
	}
	_ = ptyFD // PTY FD is used during Firecracker spawn
	return cc, nil
}

// Signal handler setup
func SetupSignalHandler(cancel func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		slog.Warn("Received shutdown signal")
		cancel()
	}()
}

// Compile-time check
var _ = slog.Default()
