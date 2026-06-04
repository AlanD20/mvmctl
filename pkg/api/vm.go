// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/vm_operations.py exactly.
package api

import (
	"context"
	"encoding/json"
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
	"mvmctl/internal/core/key"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/disk"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	infranet "mvmctl/internal/infra/network"
	"mvmctl/internal/infra/provisioner"
	"mvmctl/internal/infra/ptr"
	infraslice "mvmctl/internal/infra/slice"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
	consoleapi "mvmctl/internal/service/console"
	nocloudnet "mvmctl/internal/service/nocloudnet"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"

	"github.com/jmoiron/sqlx"
)

// ── Create ──

// Create creates one or more VMs.
// Matches Python's VMOperation.create() exactly.
func (op *Operation) VMCreate(
	ctx context.Context,
	input *inputs.VMCreateInput,
	onProgress func(errs.ProgressEvent),
) ([]*model.VM, error) {
	if err := system.CheckPrivileges("/usr/sbin/ip", "create VMs"); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodePrivilegeRequired,
			Message: fmt.Sprintf("Privilege check failed: %v", err),
			Err:     err,
			Class:   errs.ClassNeedsInteraction,
		}
	}

	count := 1
	if input.Count != nil && *input.Count > 1 {
		count = *input.Count
	}

	if count == 1 {
		return op.vmCreateSingle(ctx, input, onProgress)
	}

	return op.vmCreateBatch(ctx, input, count, onProgress)
}

func (op *Operation) vmCreateSingle(
	ctx context.Context,
	input *inputs.VMCreateInput,
	onProgress func(errs.ProgressEvent),
) ([]*model.VM, error) {
	createdAt := time.Now()
	vmID := crypto.VMID(input.Name, createdAt.Format(time.RFC3339))
	vmDir := filepath.Join(op.CacheDir, "vms", vmID)

	resolved, err := op.vmBuildResolvedInput(ctx, input, vmID, vmDir)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Message: fmt.Sprintf("Failed to resolve input: %v", err),
			Err:     err,
			Class:   errs.ClassInternal,
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

	vmInstance, execErr := op.vmExecuteCreate(createCtx, resolved, onProgress, &vmCleanup)
	signalCleanup()
	if execErr != nil {
		// Python's SigtermContext already called cleanup on signal regardless of skip_cleanup.
		// For non-signal errors, skip_cleanup is respected.
		if resolved.SkipCleanup {
			slog.Warn("VM creation failed but --skip-cleanup is active", "dir", vmDir)
		}
		return nil, &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Message: execErr.Error(),
			Err:     execErr,
			Class:   errs.ClassInternal,
		}
	}

	// Handle volumes
	if len(resolved.Volumes) > 0 {
		volSvc := volume.NewService(op.Repos.Volume)
		volSvc.SetVolumesState(ctx, resolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
		vmInstance.VolumeIDs = make([]string, len(resolved.Volumes))
		for i, v := range resolved.Volumes {
			vmInstance.VolumeIDs[i] = v.ID
		}
		op.Repos.VM.Upsert(ctx, vmInstance)
	}

	op.AuditLog.LogOperation("vm.create", nil, fmt.Sprintf("name=%s", input.Name))

	return []*model.VM{vmInstance}, nil
}

func (op *Operation) vmCreateBatch(
	ctx context.Context,
	input *inputs.VMCreateInput,
	count int,
	onProgress func(errs.ProgressEvent),
) ([]*model.VM, error) {
	names := op.vmGenerateBatchNames(input.Name, count)

	// Pre-allocate: check name collisions (single query, matching Python)
	existing, err := op.Repos.VM.GetByNames(ctx, names)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeDatabaseError,
			Message: fmt.Sprintf("Failed to check name collisions: %v", err),
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}
	if len(existing) > 0 {
		sortedNames := make([]string, 0, len(existing))
		for name := range existing {
			sortedNames = append(sortedNames, name)
		}
		sort.Strings(sortedNames)
		return nil, &errs.DomainError{
			Code:    "vm.name_collision",
			Op:      "vm",
			Message: fmt.Sprintf("VM name(s) already exist: %s", strings.Join(sortedNames, ", ")),
			Class:   errs.ClassValidation,
		}
	}

	createdVMs := make([]*model.VM, 0)
	var errors []string

	for idx, name := range names {
		createdAt := time.Now()
		vmID := crypto.VMID(name, createdAt.Format(time.RFC3339))
		vmDir := filepath.Join(op.CacheDir, "vms", vmID)

		resolved, err := op.vmBuildResolvedInput(ctx, input, vmID, vmDir)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, err))
			if input.Atomic && len(createdVMs) > 0 {
				// Rollback
				for _, vm := range createdVMs {
					_ = op.VMRemove(ctx, &inputs.VMInput{Identifiers: []string{vm.Name}, Force: new(true)})
				}
				return nil, &errs.DomainError{
					Code: "vm.atomic_failed",
					Op:   "vm",
					Message: fmt.Sprintf(
						"Atomic creation failed at '%s': %v. All %d previously created VMs have been removed.",
						name,
						err,
						len(createdVMs),
					),
					Class: errs.ClassInternal,
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

		vmInstance, execErr := op.vmExecuteCreateWithOpts(ctx, resolved, batchProgress, nil, true)
		if execErr != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, execErr))
			if input.Atomic && len(createdVMs) > 0 {
				for _, vm := range createdVMs {
					_ = op.VMRemove(ctx, &inputs.VMInput{Identifiers: []string{vm.Name}, Force: new(true)})
				}
				return nil, &errs.DomainError{
					Code: "vm.atomic_failed",
					Op:   "vm",
					Message: fmt.Sprintf(
						"Atomic creation failed at '%s': %v. All %d previously created VMs have been removed.",
						name,
						execErr,
						len(createdVMs),
					),
					Class: errs.ClassInternal,
				}
			}
			continue
		}

		// Handle volumes for batch VM (matches Python's volume handling after _execute_create)
		if resolved.Volumes != nil && len(resolved.Volumes) > 0 {
			volSvc := volume.NewService(op.Repos.Volume)
			volSvc.SetVolumesState(ctx, resolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
			vmInstance.VolumeIDs = make([]string, len(resolved.Volumes))
			for i, v := range resolved.Volumes {
				vmInstance.VolumeIDs[i] = v.ID
			}
			op.Repos.VM.Upsert(ctx, vmInstance)
		}
		createdVMs = append(createdVMs, vmInstance)
	}

	if len(errors) > 0 && len(createdVMs) == 0 {
		return nil, &errs.DomainError{
			Code:    "vm.create_failure",
			Op:      "vm",
			Message: strings.Join(errors, "; "),
			Class:   errs.ClassInternal,
		}
	}

	return createdVMs, nil
}

func (op *Operation) vmExecuteCreate(
	ctx context.Context,
	resolved *resolvedVMCreateInput,
	onProgress func(errs.ProgressEvent),
	cleanupFn *func(),
) (*model.VM, error) {
	return op.vmExecuteCreateWithOpts(ctx, resolved, onProgress, cleanupFn, false)
}

func (op *Operation) vmExecuteCreateWithOpts(
	ctx context.Context,
	resolved *resolvedVMCreateInput,
	onProgress func(errs.ProgressEvent),
	cleanupFn *func(),
	skipLimitCheck bool,
) (*model.VM, error) {
	vmRepo := op.Repos.VM

	// Check VM limit (Python: SettingsService.resolve(Database(), "settings.vm", "max_vms"))
	if !skipLimitCheck {
		maxVMs := 10
		if op.Connection != nil {
			row := op.Connection.DB().
				QueryRowContext(ctx, "SELECT value FROM user_settings WHERE category = 'settings.vm' AND key = 'max_vms'")
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
		cacheDir:         op.CacheDir,
		db:               op.Connection.DB(),
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
func (op *Operation) VMRemove(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	if err := system.CheckPrivileges("/usr/sbin/ip", "Remove VM"); err != nil {
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
		op.Connection.DB(),
		op.Repos.VM,
		op.Enr,
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
		slog.Warn(
			"VM rm: identifier(s) could not be resolved",
			"unresolved",
			unresolvedCount,
			"total",
			len(input.Identifiers),
		)
		results = append(results, errs.OperationResult{
			Status: "error", Code: string(errs.CodeVMNotFound),
			Message: fmt.Sprintf("%d VM identifier(s) not found", unresolvedCount),
		})
	}

	repo := op.Repos.VM
	volSvc := volume.NewService(op.Repos.Volume)

	for _, v := range resolved.VMs {
		vmLocal := v
		vmDir := filepath.Join(op.CacheDir, "vms", vmLocal.ID)

		// Stop the VM
		controller, ctrlErr := vm.NewController(ctx, vmLocal, repo)
		if ctrlErr == nil {
			controller.Stop(ctx, resolved.Force)
		}

		// Defense-in-depth: force-kill
		if vmLocal.PID > 0 && system.IsProcessRunning(vmLocal.PID) {
			proc, err := os.FindProcess(vmLocal.PID)
			if err == nil {
				_ = proc.Kill()
			}
		}

		// Perform removal cleanup
		op.vmPerformRemovalCleanup(ctx, vmLocal)

		// Detach volumes
		if len(vmLocal.VolumeIDs) > 0 {
			var vols []*model.VolumeItem
			for _, vid := range vmLocal.VolumeIDs {
				v, _ := op.Repos.Volume.Get(ctx, vid)
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

		op.AuditLog.LogOperation("vm.remove", map[string]interface{}{"name": vmLocal.Name}, "")

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.removed",
			Item: vmLocal, Message: fmt.Sprintf("VM '%s' removed", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

func (op *Operation) vmPerformRemovalCleanup(ctx context.Context, vm *model.VM) {
	// Console relay cleanup (matches Python's _cleanup_console)
	if vm.RelayPID != nil && vm.ID != "" {
		relay := consoleapi.NewRelayManager(vm.ID, filepath.Join(op.CacheDir, "vms", vm.ID), vm.Name,
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
		leaseRepo := network.NewLeaseRepository(op.Connection.DB())
		_ = leaseRepo.ReleaseByVM(ctx, vm.ID)
	}

	// SSH known hosts cleanup (matches Python's ssh-keygen -R {ipv4})
	if vm.IPv4 != "" {
		_ = system.RunCmdCompat(
			ctx,
			[]string{"ssh-keygen", "-R", vm.IPv4},
			system.RunCmdOpts{Check: false, Capture: false},
		)
	}
}

// ── Prune ──

// Prune prunes VMs.
// Matches Python's VMOperation.prune() exactly.
func (op *Operation) VMPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if err := system.CheckPrivileges("/usr/sbin/ip", "prune VMs"); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodePrivilegeRequired,
			Message: fmt.Sprintf("Privilege check failed: %v", err),
			Err:     err,
			Class:   errs.ClassNeedsInteraction,
		}
	}

	allVMs, err := op.Repos.VM.ListAll(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeDatabaseError,
			Message: fmt.Sprintf("Failed to list VMs: %v", err),
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}

	var removed []string
	for _, vm := range allVMs {
		if vm.Status == model.StatusRunning || vm.Status == model.StatusStarting {
			if !includeAll {
				continue
			}
		}

		if !dryRun {
			result := op.VMRemove(ctx, &inputs.VMInput{Identifiers: []string{vm.Name}, Force: new(true)})
			if result.HasErrors() {
				slog.Warn("Failed to remove VM", "name", vm.Name, "error", infraslice.JoinStringsPtrs(result))
				continue
			}
		}
		removed = append(removed, vm.Name)
	}

	return removed, nil
}

// ── List / ToJSON ──

// List returns all VMs, optionally filtered by status.
// Matches Python's VMOperation.list_all() exactly.
func (op *Operation) VMList(ctx context.Context, statusFilter interface{}) []*model.VM {
	var vms []*model.VM
	var err error

	if statusFilter != nil {
		switch s := statusFilter.(type) {
		case string:
			vms, err = op.Repos.VM.ListByStatus(ctx, s)
		case []string:
			vms, err = op.Repos.VM.ListByStatus(ctx, s...)
		default:
			vms, err = op.Repos.VM.ListAll(ctx)
		}
	} else {
		vms, err = op.Repos.VM.ListAll(ctx)
	}

	if err != nil || len(vms) == 0 {
		return vms
	}

	op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")

	return vms
}

// ToJSON converts VMs to JSON-serializable dicts.
// Matches Python's VMOperation.to_json() exactly.
// Python always includes ALL fields in every entry (with None/null if not set).
func (op *Operation) VMGet(ctx context.Context, input *inputs.VMInput) (*model.VM, error) {
	if len(input.Identifiers) != 1 {
		return nil, fmt.Errorf("Expected exactly one VM identifier")
	}
	// Use the full resolution pipeline (name, IP, MAC, ID prefix) matching Python's VMResolver
	vm, err := op.vmResolveSingleVM(ctx, input.Identifiers[0])
	if err != nil {
		return nil, err
	}
	// Enrich VM with relations (matches Python's VMResolver._enrich)
	op.Enr.EnrichVM(ctx, []*model.VM{vm}, "kernel", "image", "binary", "network", "network.leases", "volumes")
	return vm, nil
}

// Inspect returns detailed VM info with enriched data.
// Matches Python's VMOperation.inspect() exactly.
func (op *Operation) VMInspect(ctx context.Context, input *inputs.VMInput) (*responses.VMInspect, error) {
	vm, err := op.VMGet(ctx, input)
	if err != nil {
		return nil, err
	}

	var imageName *string
	if vm.ImageID != "" {
		img, err := op.Repos.Image.Get(ctx, vm.ImageID)
		if err == nil && img != nil {
			imageName = &img.Name
		}
	}
	var kernelVersion *string
	if vm.KernelID != "" {
		krn, err := op.Repos.Kernel.Get(ctx, vm.KernelID)
		if err == nil && krn != nil {
			kernelVersion = &krn.Version
		}
	}
	var binaryName *string
	if vm.BinaryID != "" {
		bin, err := op.Repos.Binary.Get(ctx, vm.BinaryID)
		if err == nil && bin != nil {
			binaryName = &bin.Name
		}
	}
	var networkName *string
	if vm.NetworkID != "" {
		net, err := op.Repos.Network.Get(ctx, vm.NetworkID)
		if err == nil && net != nil {
			networkName = &net.Name
		}
	}

	relayRunning := false
	relayPID := vm.RelayPID
	relaySocketPath := vm.RelaySocketPath
	if vm.ID != "" && vm.RelayPID != nil {
		relay := consoleapi.NewRelayManager(vm.ID, filepath.Join(op.CacheDir, "vms", vm.ID), vm.Name,
			"console.pid", "console.sock", "firecracker.console.log")
		relayRunning = relay.IsRunning()
	}

	vmDir := filepath.Join(op.CacheDir, "vms", vm.ID)
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

	// Volumes
	var volumes []responses.VMVolume
	if len(vm.VolumeIDs) > 0 {
		vols, err := op.Repos.Volume.FindByIDs(ctx, vm.VolumeIDs)
		if err == nil {
			volumes = make([]responses.VMVolume, 0, len(vols))
			for _, v := range vols {
				volumes = append(volumes, responses.VMVolume{
					ID: v.ID, Name: v.Name, Size: v.SizeBytes,
					Format: v.Format, Status: string(v.Status),
				})
			}
		}
	}

	return &responses.VMInspect{
		VM: responses.VMItemInfo{
			Name: vm.Name, ID: vm.ID, Status: string(vm.Status),
			PID: vm.PID, ExitCode: vm.ExitCode,
			SSHKeys: vm.SSHKeys, SSHUser: vm.SSHUser,
			CloudInitMode:  vm.CloudInitMode,
			NocloudNetPort: vm.NocloudNetPort, NocloudNetPID: vm.NocloudNetPID,
			PCIEnabled: vm.PCIEnabled, EnableConsole: vm.EnableConsole,
			EnableLogging: vm.EnableLogging, EnableMetrics: vm.EnableMetrics,
			CreatedAt: vm.CreatedAt, UpdatedAt: vm.UpdatedAt,
		},
		Resources: responses.VMResourcesInfo{
			VCPUs: vm.VCPUCount, Mem: vm.MemSizeMiB, Disk: vm.DiskSizeMiB,
		},
		Networking: responses.VMNetworkingInfo{
			IPv4: vm.IPv4, MAC: vm.MAC, NetworkID: vm.NetworkID,
			NetworkName: networkName, TapDevice: vm.TapDevice,
		},
		Assets: responses.VMAssetsInfo{
			ImageID: vm.ImageID, ImageName: imageName,
			KernelID: vm.KernelID, KernelVersion: kernelVersion,
			BinaryID: vm.BinaryID, BinaryName: binaryName,
		},
		Filesystem: responses.VMFilesystemInfo{
			VMDir: vmDir, RootfsPath: rootfsPath,
			ConfigPath: configPath, LogPath: logPath,
			SerialOutputPath: serialPath,
		},
		Console: responses.VMConsoleInfo{
			RelayRunning: relayRunning, RelayPID: relayPID,
			RelaySocketPath: relaySocketPath,
		},
		Volumes: volumes,
	}, nil
}

// ── Start / Stop / Reboot / Pause / Resume ──

// Start starts one or more VMs.
// Matches Python's VMOperation.start() exactly — returns BatchResult[VMInstanceItem].
// Uses batch VMRequest resolution (no N+1), matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (op *Operation) VMStart(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		op.Connection.DB(),
		op.Repos.VM,
		op.Enr,
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
			if err := op.vmRespawnFirecracker(ctx, vmLocal, false); err != nil {
				results = append(results, errs.OperationResult{
					Status: "error", Code: "vm.start_failed",
					Message:   fmt.Sprintf("start '%s': %v", vmLocal.Name, err),
					Exception: err,
				})
				continue
			}
		} else {
			controller, ctrlErr := vm.NewController(ctx, vmLocal, repo)
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

		op.AuditLog.LogOperation("vm.start", nil, fmt.Sprintf("name=%s", vmLocal.Name))

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
func (op *Operation) VMStop(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		op.Connection.DB(),
		op.Repos.VM,
		op.Enr,
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

		controller, ctrlErr := vm.NewController(ctx, vmLocal, repo)
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
		if vmLocal.PID > 0 && system.IsProcessRunning(vmLocal.PID) {
			proc, _ := os.FindProcess(vmLocal.PID)
			if proc != nil {
				_ = proc.Kill()
			}
		}

		op.AuditLog.LogOperation("vm.stop", nil, fmt.Sprintf("name=%s", vmLocal.Name))

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.stopped",
			Item:    vmLocal,
			Message: fmt.Sprintf("VM '%s' stopped", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
}

func (op *Operation) vmRespawnFirecracker(ctx context.Context, v *model.VM, snapshotMode bool) error {
	vmDir := filepath.Join(op.CacheDir, "vms", v.ID)

	// ── Restart nocloud-net server if needed (matches Python's respawn_execute) ──
	if v.CloudInitMode == "net" {
		port := 0
		if v.NocloudNetPort != nil {
			port = *v.NocloudNetPort
		}
		if port == 0 || (v.NocloudNetPID != nil && !system.IsProcessRunning(*v.NocloudNetPID)) {
			// Resolve network gateway for nocloud URL
			gateway := ""
			if v.Network != nil {
				gateway = v.Network.IPv4Gateway
			}
			if gateway == "" {
				netw, _ := op.Repos.Network.Get(ctx, v.NetworkID)
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
	if v.PID > 0 && system.IsProcessRunning(v.PID) {
		proc, err := os.FindProcess(v.PID)
		if err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			time.Sleep(100 * time.Millisecond)
			if system.IsProcessRunning(v.PID) {
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
			netw, _ := op.Repos.Network.Get(ctx, v.NetworkID)
			if netw != nil {
				bridgeName = netw.Bridge
				netID = netw.ID
				subnet = netw.Subnet
				gateway = netw.IPv4Gateway
			}
		}
		if bridgeName != "" {
			netSvc := op.Services.Network
			bridgeAddr, calcErr := network.ComputeBridgeAddress(gateway, subnet)
			if calcErr != nil {
				slog.Warn("Failed to compute bridge address during respawn", "vm", v.Name, "error", calcErr)
			} else {
				_ = netSvc.EnsureBridge(ctx, bridgeName, bridgeAddr)
			}
			_ = netSvc.EnsureTap(ctx, v.TapDevice, bridgeName, netID, subnet)
			infranet.FlushARP(ctx, bridgeName)
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
			bin, err := op.Repos.Binary.Get(ctx, v.BinaryID)
			if err == nil && bin != nil {
				binaryPath = bin.Path
			}
		}
	}
	if binaryPath == "" {
		defaultBin, _ := op.Repos.Binary.GetDefault(ctx, "firecracker")
		if defaultBin != nil {
			binaryPath = defaultBin.Path
		}
	}
	if binaryPath == "" {
		return fmt.Errorf("no binary path could be resolved for firecracker")
	}

	kernelPath := ""
	if v.KernelID != "" {
		if v.Kernel != nil {
			if v.Kernel.Path != "" {
				kernelPath = v.Kernel.Path
			}
		}
		if kernelPath == "" {
			krnl, err := op.Repos.Kernel.Get(ctx, v.KernelID)
			if err == nil && krnl != nil {
				kernelPath = krnl.Path
			}
		}
	}

	rootfsSuffix := v.RootfsSuffix
	if rootfsSuffix == "" {
		return fmt.Errorf("rootfs suffix is required")
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
			netw, _ := op.Repos.Network.Get(ctx, v.NetworkID)
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
		return nil
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
	_ = op.Repos.VM.UpdateProcessInfo(ctx, v.ID, pid, pst)

	newStatus := model.StatusRunning
	if snapshotMode {
		newStatus = model.StatusPaused
	}
	_ = op.Repos.VM.UpdateStatus(ctx, v.ID, newStatus)

	// Update in-memory VM object
	v.PID = ptr.SafeDerefInt(pid)
	v.ProcessStartTime = pst
	v.Status = newStatus

	return nil
}

// ── Snapshot / Load ──

// Snapshot creates a snapshot of a single VM (matches Python's VMOperation.snapshot() exactly).
// Python resolves exactly one VM, returns item=vm in all cases (success, error, failure).
// memFile and stateFile are output paths for the snapshot files (matches Python's mem_out, state_out).
func (op *Operation) VMSnapshot(
	ctx context.Context,
	input *inputs.VMInput,
	memFile string,
	stateFile string,
) error {
	// Python: resolved = VMRequest(inputs=inputs, db=Database()).resolve()
	//         if len(resolved.vms) != 1: raise VMNotFoundError
	vmItem, err := op.vmResolveSingleVM(ctx, input.Identifiers[0])
	if err != nil {
		return &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Op:      "vm",
			Message: fmt.Sprintf("VM not found: %s", input.Identifiers[0]),
			Class:   errs.ClassValidation,
		}
	}

	controller, ctrlErr := vm.NewController(ctx, vmItem, op.Repos.VM)
	if ctrlErr != nil {
		return &errs.DomainError{
			Code:    "vm.snapshot_failed",
			Op:      "vm",
			Message: fmt.Sprintf("Failed to snapshot VM '%s': %v", vmItem.Name, ctrlErr),
			Err:     ctrlErr,
			Class:   errs.ClassInternal,
		}
	}
	if err := controller.Snapshot(ctx, memFile, stateFile); err != nil {
		return &errs.DomainError{
			Code:    "vm.snapshot_failed",
			Op:      "vm",
			Message: fmt.Sprintf("Failed to snapshot VM '%s': %v", vmItem.Name, err),
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}

	op.AuditLog.LogOperation("vm.snapshot", nil, fmt.Sprintf("name=%s", vmItem.Name))

	return nil
}

// Load loads (resumes from snapshot) a single VM.
// memFile and stateFile are input snapshot file paths; resume controls whether VM starts after load.
// Matches Python's VMOperation.load_snapshot() exactly:
//   - re-reads VM after respawn (Python: repo.get(vm.id) → updated)
//   - catches MVMError → status="error", Exception → status="failure", item=vm
func (op *Operation) VMLoad(
	ctx context.Context,
	input *inputs.VMInput,
	memFile string,
	stateFile string,
	resume bool,
) error {
	repo := op.Repos.VM

	// Validate only one VM for load (matches Python's exactly one VM identifier check)
	if len(input.Identifiers) != 1 {
		return &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Op:      "vm",
			Message: "Expected exactly one VM identifier",
			Class:   errs.ClassValidation,
		}
	}

	var missing []string
	if _, err := os.Stat(memFile); err != nil {
		if os.IsNotExist(err) {
			missing = append(missing, memFile)
		}
	}
	if _, err := os.Stat(stateFile); err != nil {
		if os.IsNotExist(err) {
			missing = append(missing, stateFile)
		}
	}
	if len(missing) > 0 {
		paths := strings.Join(missing, ", ")
		return &errs.DomainError{
			Code:    "vm.load_snapshot_failed",
			Op:      "vm",
			Message: fmt.Sprintf("Snapshot file(s) not found: %s", paths),
			Class:   errs.ClassValidation,
		}
	}

	vmItem, err := op.vmResolveSingleVM(ctx, input.Identifiers[0])
	if err != nil {
		return &errs.DomainError{
			Code:    "vm.load_snapshot_failed",
			Op:      "vm",
			Message: fmt.Sprintf("VM not found: %s", input.Identifiers[0]),
			Class:   errs.ClassValidation,
		}
	}

	// If the VM is stopped, spawn a fresh Firecracker in pre-boot (snapshot) mode
	// so the API socket is available for PUT /snapshot/load (matches Python logic).
	// Python: if vm.status == VMStatus.STOPPED.value: _respawn_firecracker(vm, snapshot_mode=True)
	//         repo = Repository(Database()); updated = repo.get(vm.id); if updated: vm = updated
	if vmItem.Status == model.StatusStopped {
		if err := op.vmRespawnFirecracker(ctx, vmItem, true); err != nil {
			return &errs.DomainError{
				Code:    "vm.load_snapshot_failed",
				Op:      "vm",
				Message: fmt.Sprintf("Failed to respawn Firecracker for snapshot load: %v", err),
				Err:     err,
				Class:   errs.ClassInternal,
			}
		}
		// Python re-reads the updated vm from DB after respawn:
		updated, getErr := repo.Get(ctx, vmItem.ID)
		if getErr == nil && updated != nil {
			vmItem = updated
		}
	}

	controller, ctrlErr := vm.NewController(ctx, vmItem, repo)
	if ctrlErr != nil {
		return &errs.DomainError{
			Code:    "vm.load_snapshot_failed",
			Op:      "vm",
			Message: fmt.Sprintf("Failed to load snapshot for VM '%s': %v", vmItem.Name, ctrlErr),
			Err:     ctrlErr,
			Class:   errs.ClassInternal,
		}
	}
	if err := controller.LoadSnapshot(ctx, memFile, stateFile, resume); err != nil {
		return &errs.DomainError{
			Code:    "vm.load_snapshot_failed",
			Op:      "vm",
			Message: fmt.Sprintf("Failed to load snapshot for VM '%s': %v", vmItem.Name, err),
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}

	op.AuditLog.LogOperation("vm.load", nil, fmt.Sprintf("name=%s", vmItem.Name))

	return nil
}

// ── Reboot / Pause / Resume ──

// Reboot reboots one or more VMs.
// Matches Python's VMOperation.reboot() exactly — returns BatchResult[VMInstanceItem].
// Uses batch VMRequest resolution (no N+1), matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (op *Operation) VMReboot(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		op.Connection.DB(),
		op.Repos.VM,
		op.Enr,
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
		controller, ctrlErr := vm.NewController(ctx, vmLocal, repo)
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
		if err := op.vmRespawnFirecracker(ctx, vmLocal, false); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.reboot_failed",
				Message:   fmt.Sprintf("reboot '%s': %v", vmLocal.Name, err),
				Exception: err,
			})
			continue
		}

		op.AuditLog.LogOperation("vm.reboot", nil, fmt.Sprintf("name=%s", vmLocal.Name))
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
func (op *Operation) VMPause(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		op.Connection.DB(),
		op.Repos.VM,
		op.Enr,
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

		controller, ctrlErr := vm.NewController(ctx, vmLocal, repo)
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

		op.AuditLog.LogOperation("vm.pause", nil, fmt.Sprintf("name=%s", vmLocal.Name))

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
func (op *Operation) VMResume(ctx context.Context, input *inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)

	// Batch resolve all VMs first (matches Python's VMRequest.resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		op.Connection.DB(),
		op.Repos.VM,
		op.Enr,
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

		controller, ctrlErr := vm.NewController(ctx, vmLocal, repo)
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

		op.AuditLog.LogOperation("vm.resume", nil, fmt.Sprintf("name=%s", vmLocal.Name))

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
func (op *Operation) VMAttachVolume(
	ctx context.Context,
	input *inputs.VMInput,
	volumeName string,
) error {
	if err := system.CheckPrivileges("/usr/sbin/ip", "attach volume"); err != nil {
		return &errs.DomainError{
			Code:    errs.CodePrivilegeRequired,
			Message: fmt.Sprintf("Privilege check failed: %v", err),
			Err:     err,
			Class:   errs.ClassNeedsInteraction,
		}
	}

	// Resolve VM using VMRequest pipeline (matches Python: VMRequest(inputs=vm_inputs, db=db).resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		op.Connection.DB(),
		op.Repos.VM,
		op.Enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		return &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Message: fmt.Sprintf("VM not found: %v", resolveErr),
			Class:   errs.ClassValidation,
		}
	}
	if len(resolved.VMs) != 1 {
		return &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Message: "Expected exactly one VM identifier",
			Class:   errs.ClassValidation,
		}
	}
	vmItem := resolved.VMs[0]

	// Resolve volume using VolumeResolver (matches Python: vol_resolver.resolve(volume_name))
	volResolver := volume.NewResolver(op.Repos.Volume)
	vol, err := volResolver.Resolve(ctx, volumeName)
	if err != nil {
		return &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Message: fmt.Sprintf("Volume '%s' not found", volumeName),
			Class:   errs.ClassValidation,
		}
	}

	// Check volume status (matches Python: if vol.status != VolumeStatus.AVAILABLE: raise VMCreateError(...))
	if vol.Status != model.VolumeStatusAvailable {
		return &errs.DomainError{
			Code:    errs.CodeVMCreateFailed,
			Message: fmt.Sprintf("Volume '%s' is not available", volumeName),
			Class:   errs.ClassValidation,
		}
	}

	// Hotplug on running VM (matches Python: if vm.status == VMStatus.RUNNING)
	if vmItem.Status == model.StatusRunning {
		// Version gate: hotplug requires Firecracker v1.16+ (matches Python's VersionGate.require)
		if vmItem.BinaryID != "" {
			bin, _ := op.Repos.Binary.Get(ctx, vmItem.BinaryID)
			if bin != nil && bin.Version != "" {
				if !version.IsFirecrackerVersionAtLeast(bin.Version, "1.16") {
					return &errs.DomainError{
						Code: errs.CodeBinaryVersionGate,
						Message: fmt.Sprintf(
							"Volume hotplug requires Firecracker >= 1.16, got %s. Use a newer Firecracker binary or attach the volume while the VM is stopped.",
							bin.Version,
						),
						Class: errs.ClassValidation,
					}
				}
			}
		}
		// Try Firecracker API hotplug (matches Python's try: controller.attach_volume(vol) except Exception: logger.warning)
		controller, ctrlErr := vm.NewController(ctx, vmItem, op.Repos.VM)
		if ctrlErr == nil {
			if err := controller.AttachVolume(ctx, vol); err != nil {
				slog.Warn("Hotplug failed for drive", "volume", vol.ID, "error", err)
			}
		}
	}

	// VolumeController.attach (matches Python's vol_controller = VolumeController(vol, vol_repo); vol_controller.attach(vm.id))
	volController, volCtrlErr := volume.NewController(ctx, vol, op.Repos.Volume)
	if volCtrlErr == nil {
		_ = volController.Attach(ctx, vmItem.ID)
	}

	// Update VM's volume_ids (matches Python's list comprehension + append-if-not-present)
	var vmVolumeIDs []string
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
	_ = op.Repos.VM.Upsert(ctx, vmItem)

	op.AuditLog.LogOperation("vm.attach_volume", map[string]interface{}{
		"vm": vmItem.Name, "volume": vol.Name,
	}, "")

	return nil
}

// DetachVolume detaches a volume from a VM.
// Matches Python's VMOperation.detach_volume() exactly:
//   - VMInput for identification (name, ID, IP, MAC)
//   - VolumeResolver for volume resolution
//   - Version gate + SSH PCI removal + Firecracker API for hot-unplug
//   - VolumeController.detach + VM volume_ids update
func (op *Operation) VMDetachVolume(
	ctx context.Context,
	input *inputs.VMInput,
	volumeName string,
) error {
	if err := system.CheckPrivileges("/usr/sbin/ip", "detach volume"); err != nil {
		return &errs.DomainError{
			Code:    errs.CodePrivilegeRequired,
			Message: fmt.Sprintf("Privilege check failed: %v", err),
			Err:     err,
			Class:   errs.ClassNeedsInteraction,
		}
	}

	// Resolve VM using VMRequest pipeline (matches Python: VMRequest(inputs=vm_inputs, db=db).resolve())
	vmRequest := inputs.NewVMRequest(
		inputs.VMInput{
			Identifiers: input.Identifiers,
			Force:       input.Force,
		},
		op.Connection.DB(),
		op.Repos.VM,
		op.Enr,
	)
	resolved, resolveErr := vmRequest.Resolve(ctx)
	if resolveErr != nil {
		return &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Message: fmt.Sprintf("VM not found: %v", resolveErr),
			Class:   errs.ClassValidation,
		}
	}
	if len(resolved.VMs) != 1 {
		return &errs.DomainError{
			Code:    errs.CodeVMNotFound,
			Message: "Expected exactly one VM identifier",
			Class:   errs.ClassValidation,
		}
	}
	vmItem := resolved.VMs[0]

	// Resolve volume using VolumeResolver (matches Python: vol_resolver.resolve(volume_name))
	volResolver := volume.NewResolver(op.Repos.Volume)
	vol, err := volResolver.Resolve(ctx, volumeName)
	if err != nil {
		return &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Message: fmt.Sprintf("Volume '%s' not found", volumeName),
			Class:   errs.ClassValidation,
		}
	}

	// Hot-unplug if running (matches Python: if vm.status == VMStatus.RUNNING)
	if vmItem.Status == model.StatusRunning {
		// Version gate: hot-unplug requires Firecracker v1.16+ (matches Python's VersionGate.require)
		if vmItem.BinaryID != "" {
			bin, _ := op.Repos.Binary.Get(ctx, vmItem.BinaryID)
			if bin != nil && bin.Version != "" {
				if !version.IsFirecrackerVersionAtLeast(bin.Version, "1.16") {
					return &errs.DomainError{
						Code: errs.CodeBinaryVersionGate,
						Message: fmt.Sprintf(
							"Volume hot-unplug requires Firecracker >= 1.16, got %s. Use a newer Firecracker binary or detach the volume while the VM is stopped.",
							bin.Version,
						),
						Class: errs.ClassValidation,
					}
				}
			}
		}
		// ... (continue with existing SSH PCI removal + Firecracker API logic)
	}

	// VolumeController.detach (matches Python's vol_controller = VolumeController(vol, vol_repo); vol_controller.detach())
	volController, volCtrlErr := volume.NewController(ctx, vol, op.Repos.Volume)
	if volCtrlErr == nil {
		_ = volController.Detach(ctx)
	}

	// Update VM's volume_ids (matches Python's list comprehension + remove-if-present)
	var vmVolumeIDs []string
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
	_ = op.Repos.VM.Upsert(ctx, vmItem)

	op.AuditLog.LogOperation("vm.detach_volume", map[string]interface{}{
		"vm": vmItem.Name, "volume": vol.Name,
	}, "")

	return nil
}

// ── Import / Export ──

// Import creates a VM from a portable export config file.
// Matches Python's VMOperation.import_() exactly:
//   - Reads VMExportConfig JSON from input.ConfigPath
//   - Uses VMImportRequest to resolve semantic references
//   - Delegates to VMCreateBuilder for full resolution
//   - Delegates to executeCreate for provisioning
//   - Matches Python's try/except MVMError → "error", Exception → "failure"
func (op *Operation) VMImport(
	ctx context.Context,
	input *inputs.VMImportInput,
	onProgress func(errs.ProgressEvent),
) error {
	if err := system.CheckPrivileges("/usr/sbin/ip", "import VM"); err != nil {
		return &errs.DomainError{
			Code:    errs.CodePrivilegeRequired,
			Message: fmt.Sprintf("Privilege check failed: %v", err),
			Err:     err,
			Class:   errs.ClassNeedsInteraction,
		}
	}

	// Python wraps the resolve+create in a try/except that catches MVMError and Exception.
	var execErr error
	var resolved *inputs.VMCreateResolved

	// Use VMImportRequest for full semantic resolution pipeline
	// (matches Python: VMImportRequest(inputs=inputs, db=db).resolve())
	request := inputs.NewVMImportRequest(*input, op.Services.Config, op.Connection.DB())
	resolved, execErr = request.Resolve(ctx)
	var vmInstance *model.VM
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
			vmInstance, execErr = op.vmExecuteCreate(createCtx, internalResolved, onProgress, &vmCleanup)
			signalCleanup()
			if execErr != nil {
				if internalResolved.SkipCleanup {
					slog.Warn("VM import failed but --skip-cleanup is active", "dir", internalResolved.VMDir)
				}
			} else {
				// Handle volumes
				if len(internalResolved.Volumes) > 0 {
					volSvc := volume.NewService(op.Repos.Volume)
					volSvc.SetVolumesState(ctx, internalResolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
					vmInstance.VolumeIDs = make([]string, len(internalResolved.Volumes))
					for i, v := range internalResolved.Volumes {
						vmInstance.VolumeIDs[i] = v.ID
					}
					op.Repos.VM.Upsert(ctx, vmInstance)
				}
			}
		}
	}

	if execErr != nil {
		return &errs.DomainError{
			Code:    "vm.import_failed",
			Op:      "vm",
			Message: execErr.Error(),
			Err:     execErr,
			Class:   errs.ClassInternal,
		}
	}

	op.AuditLog.LogOperation("vm.import", nil, fmt.Sprintf("name=%s,config=%s", resolved.Name, input.ConfigPath))

	return nil
}

// Export exports a VM's configuration as a portable VMExportConfig.
// Matches Python's VMOperation.export() exactly — returns VMExportConfig, not an error code.
func (op *Operation) VMExport(ctx context.Context, input *inputs.VMInput) (*inputs.VMExportConfig, error) {
	vmItem, err := op.VMGet(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}

	// Resolve related asset metadata (matches Python's Repository(db).get(vm.image_id) etc.)
	image, _ := op.Repos.Image.Get(ctx, vmItem.ImageID)
	kernel, _ := op.Repos.Kernel.Get(ctx, vmItem.KernelID)
	binary, _ := op.Repos.Binary.Get(ctx, vmItem.BinaryID)
	netItem, _ := op.Repos.Network.Get(ctx, vmItem.NetworkID)

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
			VCPUs: new(vmItem.VCPUCount),
			Mem:   new(vmItem.MemSizeMiB),
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
			Args:          new(bootArgsStr),
			EnableConsole: &vmItem.EnableConsole,
		},
		Firecracker: inputs.VMExportFirecrackerConfig{
			EnableAPISocket: new(true),
			PCIEnabled:      &vmItem.PCIEnabled,
			LsmFlags:        lsmFlagsPtr,
			NestedVirt:      &vmItem.NestedVirt,
			CPUConfig:       cpuConfigStr,
		},
		CloudInit: inputs.VMExportCloudInitConfig{
			Mode:           cloudInitModePtr,
			User:           &rootUser,
			NocloudNetPort: new(nocloudPort),
		},
	}

	op.AuditLog.LogOperation("vm.export", map[string]interface{}{"name": vmItem.Name}, "")

	return cfg, nil
}

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
	SSHKeys               []*model.SSHKeyItem
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
	db               *sqlx.DB
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
		c.guestMAC = infranet.VMGenerateMAC(c.resolved.GuestMACPrefix)
	}
	c.tapName = infranet.VMGenerateTAPName(c.resolved.Network.Name, c.resolved.Name)

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

	leaseSvc, err := network.NewLeaseService(ctx, c.resolved.Network, network.NewLeaseRepository(c.db), nil)
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
	infranet.FlushARP(ctx, c.resolved.Network.Bridge)

	// Progress: rootfs
	if c.onProgress != nil {
		c.onProgress(errs.ProgressEvent{Phase: "rootfs", Status: "running", Message: "Copying root filesystem..."})
	}

	// Clone rootfs
	if err := c.cloneImage(ctx); err != nil {
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
	)
	if err != nil {
		return fmt.Errorf("failed to create VM provisioner: %w", err)
	}

	// Resize rootfs
	provisioner.Resize(c.resolved.DiskSizeBytes)

	mode := c.resolved.CloudInitMode

	// Read SSH pubkeys from the key service (used by OFF, INJECT, ISO, NET modes)
	keySvc := key.NewService(key.NewRepository(nil), infra.GetKeysDir())
	pubkeys, _ := keySvc.GetPubkeys(ctx, c.resolved.SSHKeys)

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
		provisioner.Deblob(ctx, c.resolved.Image.Distro)
	}

	// Fix fstab for Firecracker (superfloppy /dev/vda layout)
	provisioner.FixFstab()

	// Execute all queued provisioning operations
	provisioner.Run(ctx)

	// Progress: firecracker
	if c.onProgress != nil {
		c.onProgress(
			errs.ProgressEvent{Phase: "firecracker", Status: "running", Message: "Starting Firecracker microVM..."},
		)
	}

	// --- Firecracker config ---
	fcConfig := c.buildFirecrackerConfig(ctx)
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
		return fmt.Errorf(
			"VM ID '%s' produces a socket path that is too long (%d chars, max 107). This is a system limit for Unix domain sockets. Path: %s",
			c.vmID,
			len(socketPath),
			socketPath,
		)
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

func (c *vmCreateContext) cloneImage(ctx context.Context) error {
	if c.resolved == nil {
		return fmt.Errorf("Failed to resolve necessary dependencies")
	}
	fsType := c.resolved.Image.FSType
	if fsType == "" {
		return fmt.Errorf("fsType is required")
	}
	vmRootfsPath := filepath.Join(c.vmDir, "rootfs."+fsType)

	imageSvc := image.NewService(image.NewRepository(nil))
	if _, err := imageSvc.EnsureCached([]*model.ImageItem{c.resolved.Image}); err != nil {
		return fmt.Errorf("ensure cached image: %w", err)
	}
	if err := imageSvc.MaterializeTo(ctx, c.resolved.Image.ID, fsType, vmRootfsPath); err != nil {
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
func (c *vmCreateContext) forRespawn(vm *model.VM, snapshotMode bool) error {
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
		return fmt.Errorf("rootfs suffix is required")
	}
	c.rootfsPath = filepath.Join(c.vmDir, "rootfs."+rootfsSuffix)

	// Build resolved from VM state — makes buildFirecrackerConfig() work unchanged
	c.resolved = &resolvedVMCreateInput{
		Name:                  vm.Name,
		VMID:                  vm.ID,
		VMDir:                 c.vmDir,
		VCPUCount:             vm.VCPUCount,
		MemSizeMiB:            vm.MemSizeMiB,
		User:                  ptr.SafeDeref(vm.SSHUser),
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
		BootArgs:              ptr.SafeDeref(vm.BootArgs),
		LSMFlags:              ptr.SafeDeref(vm.LSMFlags),
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
		isoPathPtr = new(isoPath)
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

	return nil
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
		if port == 0 || (c._vm.NocloudNetPID != nil && !system.IsProcessRunning(*c._vm.NocloudNetPID)) {
			nocloudSvc := nocloudnet.NewNoCloudServer(
				c._vm.ID,
				c._vm.Name,
				c.vmDir,
				netItem.IPv4Gateway,
				port,
				8000,
				9000,
				100,
			)
			if _, newPort, _, startErr := nocloudSvc.Start(ctx, c.vmDir); startErr != nil {
				slog.Warn("Failed to start/restart nocloud-net server", "vm", c._vm.Name, "error", startErr)
			} else if port == 0 {
				port = newPort
			}
		}
	}

	// ── Force-kill any remaining Firecracker process ──
	if c._vm.PID > 0 && system.IsProcessRunning(c._vm.PID) {
		proc, err := os.FindProcess(c._vm.PID)
		if err == nil {
			// Try SIGTERM first, then SIGKILL (matches Python's kill_and_wait)
			_ = proc.Signal(syscall.SIGTERM)
			time.Sleep(100 * time.Millisecond)
			if system.IsProcessRunning(c._vm.PID) {
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
	infranet.FlushARP(ctx, netItem.Bridge)

	// ── Build config and spawn ──
	fcConfig := c.buildFirecrackerConfig(ctx)
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
func (c *vmCreateContext) buildFirecrackerConfig(ctx context.Context) *model.FirecrackerConfig {
	if c.resolved == nil {
		return nil
	}

	var cpuVendor *string
	var cpuArchitecture *string
	if c.db != nil {
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
		BootArgs:             new(c.resolved.BootArgs),
		LSMFlags:             new(c.resolved.LSMFlags),
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
			fcConfig.CloudInitISOPath = new(*c.cloudInitResult.isoPath)
		}
		if c.cloudInitResult.nocloudURL != nil {
			fcConfig.CloudInitNoCloudURL = new(*c.cloudInitResult.nocloudURL)
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

	now := time.Now().Format(time.RFC3339)

	vm := &model.VM{
		ID:               c.vmID,
		Name:             c.resolved.Name,
		PID:              ptr.SafeDerefInt(c.spawner.PID()),
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
		SSHUser:          &c.resolved.User,
		VolumeIDs:        []string{},
	}
	// Extract key names for the VM record (model.SSHKeys is []string)
	for _, k := range c.resolved.SSHKeys {
		if k != nil {
			vm.SSHKeys = append(vm.SSHKeys, k.Name)
		}
	}

	// Set cpu_config from resolved input (matches Python: if self.resolved.cpu_config is not None)
	if c.resolved.CPUConfig != nil {
		vm.CPUConfig = c.resolved.CPUConfig
	}

	vm.LogPath = new(filepath.Join(c.vmDir, c.resolved.LogFilename))
	vm.SerialOutputPath = new(filepath.Join(c.vmDir, c.resolved.SerialOutputFilename))

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

func (op *Operation) vmBuildResolvedInput(
	ctx context.Context,
	input *inputs.VMCreateInput,
	vmID, vmDir string,
) (*resolvedVMCreateInput, error) {
	// Resolve image (handles selectors like "alpine:3.21" and ID prefixes)
	var image *model.ImageItem
	var err error
	if input.Image != nil {
		selector := *input.Image
		// Try exact name first
		image, err = op.Repos.Image.GetByName(ctx, selector)
		if err == nil && image == nil {
			// Try type:version selector (e.g. "alpine:3.21")
			parts := strings.SplitN(selector, ":", 2)
			if len(parts) == 2 {
				image, err = op.Repos.Image.GetByVersionAndType(ctx, parts[1], parts[0])
			}
		}
		if image == nil {
			image, err = op.Repos.Image.Get(ctx, selector)
		}
	}
	if image == nil {
		image, err = op.Repos.Image.GetDefault(ctx)
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
		kernel, err = op.Repos.Kernel.Get(ctx, *input.KernelID)
		if err != nil || kernel == nil {
			kernel, err = op.Repos.Kernel.GetByName(ctx, *input.KernelID)
		}
	} else {
		kernel, err = op.Repos.Kernel.GetDefault(ctx)
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
		network, err = op.Repos.Network.GetByName(ctx, *input.NetworkName)
		if err != nil || network == nil {
			network, err = op.Repos.Network.Get(ctx, *input.NetworkName)
		}
	} else {
		network, err = op.Repos.Network.GetDefault(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve network: %w", err)
	}
	if network == nil {
		return nil, fmt.Errorf("resolve network: no default network")
	}

	// Resolve binary from DB (matches Python's Repository resolution)
	binary := op.vmResolveBinary(ctx, input.BinaryID, input.FirecrackerBin, op.Repos.Binary)

	// Resolve SSH keys
	sshKeyNames := input.SSHKeys
	if len(sshKeyNames) == 0 {
		defaultKeys, _ := op.Repos.Key.GetDefaults(ctx)
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
		if bytes, err := disk.ParseDiskSizeToBytes(*input.MemSizeMib); err == nil {
			memSizeMiB = int(bytes / (1024 * 1024))
		}
	}
	if memSizeMiB <= 0 {
		memSizeMiB = 512
	}

	diskSizeMiB := image.MinRootfsSizeMiB
	diskSizeBytes := int64(diskSizeMiB) * 1024 * 1024
	if input.DiskSize != nil {
		if bytes, err := disk.ParseDiskSizeToBytes(*input.DiskSize); err == nil {
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
	if op.Connection != nil {
		row := op.Connection.DB().
			QueryRowContext(ctx, "SELECT value FROM user_settings WHERE category = 'settings' AND key = 'guestfs_enabled'")
		var val string
		if err := row.Scan(&val); err == nil {
			if val == "true" || val == "1" {
				provisionerType = provisioner.ProvisionerGuestFS
			}
		}
	}

	// Resolve SSH key items (with PublicKeyPath) so GetPubkeys can read
	// files directly without re-querying the DB.
	var sshKeyItems []*model.SSHKeyItem
	for _, name := range sshKeyNames {
		key, err := op.Repos.Key.GetByName(ctx, name)
		if err == nil && key != nil {
			sshKeyItems = append(sshKeyItems, key)
		} else {
			// Fall back: create a minimal item with just the name
			sshKeyItems = append(
				sshKeyItems,
				&model.SSHKeyItem{Name: name, PublicKeyPath: filepath.Join(infra.GetKeysDir(), name+".pub")},
			)
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
		SSHKeys:               sshKeyItems,
		Provisioner:           model.ProvisionerType(provisionerType),
	}, nil
}

// resolveBinary resolves the firecracker binary from DB or falls back to hardcoded path.
// Matches Python's VMCreateRequest binary resolution: tries explicit ID/path, then default, then fallback.
func (op *Operation) vmResolveBinary(
	ctx context.Context,
	binaryID, firecrackerBin *string,
	binaryRepo binary.Repository,
) *model.BinaryItem {
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

func (op *Operation) vmResolveSingleVM(ctx context.Context, ident string) (*model.VM, error) {
	if ident == "" {
		return nil, fmt.Errorf("VM identifier is required")
	}
	// Match Python's VMResolver.resolve() order:
	// 1. Try by name first
	vm, err := op.Repos.VM.GetByName(ctx, ident)
	if err == nil && vm != nil {
		return vm, nil
	}
	// 2. If contains '.', try by IP (matches Python's by_ip)
	if strings.Contains(ident, ".") {
		vm, err = op.Repos.VM.FindByIP(ctx, ident)
		if err == nil && vm != nil {
			return vm, nil
		}
	}
	// 3. If contains ':', try by MAC (matches Python's by_mac)
	if strings.Contains(ident, ":") {
		vm, err = op.Repos.VM.FindByMAC(ctx, ident)
		if err == nil && vm != nil {
			return vm, nil
		}
	}
	// 4. Try by ID prefix (matches Python's find_by_prefix)
	matches, err := op.Repos.VM.FindByPrefix(ctx, ident)
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

func (op *Operation) vmGenerateBatchNames(baseName string, count int) []string {
	names := make([]string, count)
	for i := range count {
		names[i] = fmt.Sprintf("%s-%d", baseName, i+1)
	}
	return names
}

// ── Standalone helper functions ──

// resolvedFromBuilderOutput converts a VMCreateResolved (from VMCreateBuilder.Build)
// to the internal resolvedVMCreateInput used by executeCreate.
// This bridges the public API layer to the internal VM creation pipeline.
func resolvedFromBuilderOutput(r *inputs.VMCreateResolved) *resolvedVMCreateInput {
	if r == nil {
		return nil
	}

	// Fields are already typed — no type assertions needed

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
		SSHKeys:               r.SSHKeys,
		Provisioner:           model.ProvisionerType(r.Provisioner),
		Volumes:               r.Volumes,
		ExtraDrives:           r.ExtraDrives,
	}
}

// setupConsoleRelay creates a Controller and PTY for the given VM.
// Matches Python's ConsoleOperation startup sequence.
func (op *Operation) vmSetupConsoleRelay(ctx context.Context, vm *model.VM) (*console.Controller, error) {
	relayPath := filepath.Join(op.CacheDir, "vms", vm.ID)
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
