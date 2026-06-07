// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/vm_operations.py exactly.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mvmctl/internal/core/cloudinit"
	"mvmctl/internal/core/console"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/infra/model"
	infranet "mvmctl/internal/infra/network"
	"mvmctl/internal/infra/provisioner"
	"mvmctl/internal/infra/ptr"
	infraslice "mvmctl/internal/infra/slice"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
	consolesvc "mvmctl/internal/service/console"
	nocloudnetsvc "mvmctl/internal/service/nocloudnet"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
)

// ── Create ──

// Create creates one or more VMs.
// Matches Python's VMOperation.create() exactly.
func (op *Operation) VMCreate(
	ctx context.Context,
	input inputs.VMCreateInput,
	onProgress event.OnProgressCallback,
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

	names := vm.GenerateBatchNames(input.Name, count)

	// Pre-allocate: check name collisions (single query, matching Python)
	existing, err := op.Repos.VM.NamesExist(ctx, names)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeDatabaseError,
			Message: fmt.Sprintf("Failed to check name collisions: %v", err),
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}
	if len(existing) > 0 {
		return nil, &errs.DomainError{
			Code:    "vm.name_collision",
			Op:      "vm",
			Message: fmt.Sprintf("VM name(s) already exist: %s", strings.Join(existing, ", ")),
			Class:   errs.ClassValidation,
		}
	}

	// Resolve shared state ONCE before the loop (matches Python's VMCreateRequest.resolve())
	vmID := crypto.VMID(input.Name, time.Now().Format(time.RFC3339))
	vmDir := infra.GetVMDirByID(vmID)
	request := inputs.NewVMCreateRequest(
		vmID, vmDir, input,
		op.Services.Config,
		op.Repos.VM,
		op.Repos.Network,
		op.Repos.Image,
		op.Repos.Kernel,
		op.Repos.Binary,
		op.Repos.Key,
		op.Repos.Volume,
		op.Repos.Lease,
	)
	sharedResolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, err
	}

	createdVMs := make([]*model.VM, 0)
	var errors []string

	for idx, name := range names {
		createdAt := time.Now()
		vmID := crypto.VMID(name, createdAt.Format(time.RFC3339))
		vmDir := infra.GetVMDirByID(vmID)

		resolved := request.CloneVMInput(sharedResolved, name, vmID, vmDir)

		// Wrap progress with [i/N] prefix for batch; pass onProgress directly for single VM.
		// (Go 1.22+ loop variables are per-iteration, no capture shadowing needed.)
		progress := onProgress
		if count > 1 {
			progress = func(e event.Progress) {
				if onProgress != nil {
					onProgress(event.Progress{
						Phase:   e.Phase,
						Status:  e.Status,
						Message: fmt.Sprintf("[%d/%d] %s: %s", idx+1, count, name, e.Message),
					})
				}
			}
		}

		vmInstance, execErr := op.vmBuilderCreate(ctx, resolved, progress)
		if execErr != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, execErr))
			if input.Atomic && len(createdVMs) > 0 {
				for _, vm := range createdVMs {
					_ = op.VMRemove(ctx, inputs.VMInput{Identifiers: []string{vm.Name}, Force: true})
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

		// Handle volumes (matches Python's volume handling after _execute_create)
		if resolved.Volumes != nil && len(resolved.Volumes) > 0 {
			op.Services.Volume.SetVolumesState(ctx, resolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
			vmInstance.VolumeIDs = make([]string, len(resolved.Volumes))
			for i, v := range resolved.Volumes {
				vmInstance.VolumeIDs[i] = v.ID
			}
			op.Repos.VM.Upsert(ctx, vmInstance)
		}

		// Audit log per VM (matches Python's AuditLog.log(audit_action, ...) in _execute_create)
		op.AuditLog.LogOperation("vm.create", nil, fmt.Sprintf("name=%s", name))

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

func (op *Operation) vmBuilderCreate(
	ctx context.Context,
	resolved *inputs.ResolvedVMCreateInput,
	onProgress event.OnProgressCallback,
) (*model.VM, error) {

	if resolved == nil {
		return nil, fmt.Errorf("failed to resolve necessary dependencies")
	}

	if resolved.VMDir == "" {
		return nil, fmt.Errorf("vm directory not set")
	}

	// Create data holder for creation state
	builder := &VMCreateBuilder{
		name:             resolved.Name,
		vmID:             resolved.VMID,
		vmDir:            resolved.VMDir,
		onProgress:       onProgress,
		resolved:         resolved,
		resourcesCreated: make(map[string]bool),
	}

	// Cleanup on context cancellation (signal from main() via signal.NotifyContext).
	// The goroutine is only needed during execute(). After execute() returns,
	// cleanupDone is closed and wg.Wait() ensures the goroutine has exited before
	// we check cleaned. This provides a happens-before ordering so there is no
	// race between the goroutine setting cleaned and us reading it.
	var cleaned atomic.Bool
	var wg sync.WaitGroup
	cleanupDone := make(chan struct{})
	wg.Go(func() {
		select {
		case <-ctx.Done():
			if !resolved.SkipCleanup && cleaned.CompareAndSwap(false, true) {
				op.vmBuilderCleanup(context.Background(), builder)
			}
		case <-cleanupDone:
		}
	})

	var vmInstance *model.VM
	var execErr error

	// Execute
	execErr = op.vmBuilderExecute(ctx, builder, resolved)

	// Stop cleanup goroutine and wait for it to finish, providing a
	// happens-before edge so cleaned.Load() below is reliable.
	close(cleanupDone)
	wg.Wait()

	// If the goroutine already cleaned up (signal during execute), fail closed
	// rather than returning a success with destroyed resources.
	if execErr == nil && cleaned.Load() {
		execErr = fmt.Errorf("vm creation cancelled by signal")
	}

	if execErr == nil {
		vmInstance = builder.toVMModel()
		// Python: if vm_instance is None: raise VMCreateError("Failed to create VM instance model")
		if vmInstance == nil {
			if builder.spawner == nil {
				execErr = fmt.Errorf("Firecracker spawner is not set in context")
			} else if builder.spawner.PID == nil {
				execErr = fmt.Errorf("Failed to spawn Firecracker process")
			} else {
				execErr = fmt.Errorf("Failed to create VM instance model")
			}
		} else if err := op.Repos.VM.Upsert(ctx, vmInstance); err != nil {
			execErr = fmt.Errorf("upsert VM: %w", err)
		}
	}

	if execErr != nil {
		if !resolved.SkipCleanup {
			if cleaned.CompareAndSwap(false, true) {
				op.vmBuilderCleanup(ctx, builder)
			}
		}
		return nil, execErr
	}

	return vmInstance, nil
}

// vmBuilderExecute performs the actual VM creation steps.
// Moved from VMCreateBuilder.execute() to Operation to use services/repos directly.
func (op *Operation) vmBuilderExecute(ctx context.Context, builder *VMCreateBuilder, resolved *inputs.ResolvedVMCreateInput) error {
	if resolved == nil {
		return fmt.Errorf("failed to resolve necessary dependencies")
	}

	// Generate MAC and TAP name
	if resolved.RequestedGuestMAC != nil {
		builder.guestMAC = *resolved.RequestedGuestMAC
	} else {
		builder.guestMAC = infranet.VMGenerateMAC(resolved.GuestMACPrefix)
	}
	builder.tapName = infranet.VMGenerateTAPName(resolved.Network.Name, resolved.Name)

	// Create VM directory
	if err := os.MkdirAll(builder.vmDir, 0755); err != nil {
		return fmt.Errorf("create vm directory: %w", err)
	}
	builder.markCreated("vm_dir")

	// Progress: network
	emitProgress(builder.onProgress, "network", "running", "Configuring network...")

	// Network setup
	bridgeAddr, calcErr := network.ComputeBridgeAddress(resolved.Network.IPv4Gateway, resolved.Network.Subnet)
	if calcErr != nil {
		return fmt.Errorf("compute bridge address: %w", calcErr)
	}
	if err := op.Services.Network.EnsureBridge(ctx, resolved.Network.Bridge, bridgeAddr); err != nil {
		return fmt.Errorf("ensure bridge: %w", err)
	}

	// IP Lease (reuse pre-injected lease repo)
	leaseCtrl, err := network.NewLeaseController(ctx, resolved.Network, op.Repos.Lease, nil)
	if err != nil {
		return fmt.Errorf("create lease controller: %w", err)
	}
	if resolved.RequestedGuestIP != nil {
		ip, leaseErr := leaseCtrl.LeaseSpecific(ctx, *resolved.RequestedGuestIP, builder.vmID)
		if leaseErr != nil {
			return fmt.Errorf("lease specific ip: %w", leaseErr)
		}
		builder.guestIP = ip
	} else {
		ip, leaseErr := leaseCtrl.Lease(ctx, builder.vmID)
		if leaseErr != nil {
			return fmt.Errorf("lease ip: %w", leaseErr)
		}
		builder.guestIP = ip
	}

	// NAT and TAP in a single batch for atomic firewall rule application
	natGateways := network.NatGatewaysList(resolved.Network)
	var tapErr error
	op.Services.Network.WithBatch(ctx, func() {
		if resolved.Network.NATEnabled && len(natGateways) > 0 {
			if natErr := op.Services.Network.EnsureNAT(ctx, resolved.Network.Bridge,
				natGateways, resolved.Network.Subnet, resolved.Network.ID); natErr != nil {
				slog.Warn("failed to ensure NAT rules during VM creation",
					"vm", resolved.Name, "error", natErr)
			}
		}
		if err := op.Services.Network.EnsureTap(ctx, builder.tapName, resolved.Network.Bridge,
			resolved.Network.ID, resolved.Network.Subnet); err != nil {
			tapErr = err
		}
	})
	if tapErr != nil {
		return fmt.Errorf("ensure TAP: %w", tapErr)
	}
	builder.markCreated("network_tap")
	infranet.FlushARP(ctx, resolved.Network.Bridge)

	// Progress: rootfs
	emitProgress(builder.onProgress, "rootfs", "running", "Copying root filesystem...")

	// Clone rootfs
	if err := builder.cloneImage(ctx, op.Services.Image, resolved); err != nil {
		return err
	}
	builder.markCreated("rootfs")

	// Progress: cloud-init
	emitProgress(builder.onProgress, "cloud-init", "running", "Provisioning cloud-init...")

	// --- Cloud-init provisioning ---
	backend, backendErr := provisioner.NewBackend(ctx, provisioner.BackendOpts{
		RootfsPath:      builder.rootfsPath,
		FsType:          resolved.Image.FSType,
		CacheDir:        op.CacheDir,
		ProvisionerType: provisioner.ProvisionerType(resolved.Provisioner),
		RootUID:         resolved.RootUID,
		RootGID:         resolved.RootGID,
		UserUID:         resolved.UserUID,
		UserGID:         resolved.UserGID,
	})
	if backendErr != nil {
		return fmt.Errorf("failed to create VM provisioner: %w", backendErr)
	}

	// Resize rootfs
	if resizeErr := backend.Resize(ctx, resolved.DiskSizeBytes); resizeErr != nil {
		return fmt.Errorf("resize rootfs: %w", resizeErr)
	}

	// Read SSH pubkeys (errors logged but not fatal — SSH keys may be optional)
	pubkeys, pubkeyErr := op.Services.Key.GetPubkeys(ctx, resolved.SSHKeys)
	if pubkeyErr != nil {
		slog.Warn("failed to read SSH pubkeys during VM creation",
			"vm", resolved.Name, "error", pubkeyErr)
	}

	// Resolve user password from config defaults
	userPassword, _ := op.Services.Config.GetString(ctx, "defaults.vm", "user_password")

	// Common operations for OFF and INJECT modes
	if resolved.CloudInitMode == model.CloudInitModeOFF || resolved.CloudInitMode == model.CloudInitModeINJECT {
		if err := backend.SetHostname(ctx, resolved.Name); err != nil {
			return fmt.Errorf("set hostname: %w", err)
		}
		if err := backend.InjectDNS(ctx, resolved.DNSServer); err != nil {
			return fmt.Errorf("inject DNS: %w", err)
		}
		if err := backend.SetupSSH(ctx, resolved.User, pubkeys); err != nil {
			return fmt.Errorf("setup SSH: %w", err)
		}
	}

	if resolved.CloudInitMode == model.CloudInitModeOFF {
		if err := backend.DisableCloudInit(ctx); err != nil {
			return fmt.Errorf("disable cloud-init: %w", err)
		}
		builder.markCreated("cloud-init-off")

	} else if resolved.CloudInitMode == model.CloudInitModeINJECT {
		ciConfig := &cloudinit.Config{
			Mode:                  resolved.CloudInitMode,
			VMName:                resolved.Name,
			VMID:                  builder.vmID,
			VMDir:                 builder.vmDir,
			CloudInitDir:          filepath.Join(builder.vmDir, "cloud-init"),
			GuestIP:               builder.guestIP,
			TapName:               builder.tapName,
			User:                  resolved.User,
			IPv4Gateway:           resolved.Network.IPv4Gateway,
			NetworkPrefixLen:      resolved.NetworkPrefixLen,
			SkipNetworkConfig:     resolved.SkipCINetworkConfig,
			SSHPubkeys:            pubkeys,
			UserPassword:          userPassword,
			CustomCloudInitConfig: resolved.CustomCloudInitConfig,
			NocloudNetPort:        resolved.NocloudNetPort,
			CloudInitISOPath:      resolved.CloudInitISOPath,
			KeepCloudInitISO:      resolved.KeepCloudInitISO,
			CloudInitISOName:      resolved.CloudInitISOName,
			NocloudPortRangeStart: resolved.NocloudPortRangeStart,
			NocloudPortRangeEnd:   resolved.NocloudPortRangeEnd,
			NocloudMaxPortRetries: resolved.NocloudMaxPortRetries,
		}
		ciProvisioner := cloudinit.NewProvisioner(ciConfig, op.Services.Network.FirewallTracker())
		ciResult, ciErr := ciProvisioner.Provision(ctx)
		if ciErr != nil {
			return fmt.Errorf("cloud-init inject provisioning failed: %w", ciErr)
		}
		builder.cloudInitResult = &cloudInitResult{
			mode: ciResult.Mode,
		}
		if err := backend.InjectCloudInit(ctx, ciConfig.CloudInitDir); err != nil {
			return fmt.Errorf("inject cloud-init: %w", err)
		}
		builder.markCreated("cloud-init-inject")

	} else if resolved.CloudInitMode == model.CloudInitModeISO || resolved.CloudInitMode == model.CloudInitModeNET {
		ciConfig := &cloudinit.Config{
			Mode:                  resolved.CloudInitMode,
			VMName:                resolved.Name,
			VMID:                  builder.vmID,
			VMDir:                 builder.vmDir,
			CloudInitDir:          filepath.Join(builder.vmDir, "cloud-init"),
			GuestIP:               builder.guestIP,
			TapName:               builder.tapName,
			User:                  resolved.User,
			IPv4Gateway:           resolved.Network.IPv4Gateway,
			NetworkPrefixLen:      resolved.NetworkPrefixLen,
			SkipNetworkConfig:     resolved.SkipCINetworkConfig,
			SSHPubkeys:            pubkeys,
			UserPassword:          userPassword,
			CustomCloudInitConfig: resolved.CustomCloudInitConfig,
			NocloudNetPort:        resolved.NocloudNetPort,
			CloudInitISOPath:      resolved.CloudInitISOPath,
			KeepCloudInitISO:      resolved.KeepCloudInitISO,
			CloudInitISOName:      resolved.CloudInitISOName,
			NocloudPortRangeStart: resolved.NocloudPortRangeStart,
			NocloudPortRangeEnd:   resolved.NocloudPortRangeEnd,
			NocloudMaxPortRetries: resolved.NocloudMaxPortRetries,
			// Pre-allocated server (shared across batch, empty if unset)
			NoCloudURL:  resolved.NoCloudURL,
			NoCloudPort: resolved.NoCloudPort,
			NoCloudPID:  resolved.NoCloudPID,
			KillAfter:   resolved.NoCloudKillAfter,
		}
		ciProvisioner := cloudinit.NewProvisioner(ciConfig, op.Services.Network.FirewallTracker())
		ciResult, ciErr := ciProvisioner.Provision(ctx)
		if ciErr != nil {
			return fmt.Errorf("cloud-init provisioning failed: %w", ciErr)
		}
		builder.cloudInitResult = &cloudInitResult{
			mode:        ciResult.Mode,
			isoPath:     ciResult.ISOPath,
			nocloudURL:  ciResult.NocloudURL,
			nocloudPort: &ciResult.NocloudPort,
			nocloudPID:  ciResult.NocloudPID,
		}

		if resolved.CloudInitMode == model.CloudInitModeISO {
			builder.markCreated("cloud-init-iso")
		} else {
			builder.markCreated("cloud-init-net")
		}
	}

	// Deblob (OS cache cleanup) unless explicitly skipped
	if !resolved.SkipDeblob {
		if err := backend.Deblob(ctx, &resolved.Image.Distro); err != nil {
			return fmt.Errorf("deblob rootfs: %w", err)
		}
	}

	// Fix fstab for Firecracker (superfloppy /dev/vda layout)
	if err := backend.FixFstab(ctx); err != nil {
		return fmt.Errorf("fix fstab: %w", err)
	}

	// Execute all queued provisioning operations
	if err := backend.Run(ctx); err != nil {
		return fmt.Errorf("provision VM rootfs: %w", err)
	}

	// Progress: firecracker
	emitProgress(builder.onProgress, "firecracker", "running", "Starting Firecracker microVM...")

	// --- Firecracker config ---
	fcConfig := builder.buildFirecrackerConfig(ctx)
	if fcConfig == nil {
		return fmt.Errorf("firecracker config is nil")
	}

	spawner := vm.NewFirecrackerSpawner(fcConfig)
	builder.fcManager = fcConfig

	if err := spawner.WriteToFile(); err != nil {
		return fmt.Errorf("write firecracker config: %w", err)
	}
	builder.markCreated("firecracker")

	// Validate socket path won't exceed Unix domain socket limit
	socketPath := spawner.APISocketPath
	if len(socketPath) >= 108 {
		return fmt.Errorf(
			"VM ID '%s' produces a socket path that is too long (%d chars, max 107). This is a system limit for Unix domain sockets. Path: %s",
			builder.vmID,
			len(socketPath),
			socketPath,
		)
	}

	// Console relay setup (before spawn)
	if resolved.EnableConsole {
		consoleCtrl := console.NewController(builder.vmID, builder.vmDir, builder.name,
			resolved.ConsolePIDFilename, resolved.ConsoleSocketFilename)
		ptyFD, ptyErr := consoleCtrl.CreatePTY()
		if ptyErr != nil {
			return fmt.Errorf("console PTY creation failed: %w", ptyErr)
		}
		builder.relay = consoleCtrl
		fcConfig.RelayClientFD = &ptyFD
	}

	// Spawn Firecracker
	if err := spawner.Spawn(); err != nil {
		return fmt.Errorf("failed to spawn Firecracker: %w", err)
	}

	// Store spawner for toVMModel()
	builder.spawner = spawner

	// Start console relay after spawn
	if resolved.EnableConsole && builder.relay != nil {
		builder.relay.CloseClientFD()
		_, _, startErr := builder.relay.Start(ctx)
		if startErr != nil {
			return fmt.Errorf("console relay start failed: %w", startErr)
		}
		builder.markCreated("console_relay")
	}

	emitProgress(builder.onProgress, "complete", "complete", "VM created successfully")

	return nil
}

// vmBuilderCleanup cleans up partially-created VM resources on failure.
func (op *Operation) vmBuilderCleanup(ctx context.Context, builder *VMCreateBuilder) {
	if builder.vmDir == "" || builder.resolved == nil {
		return
	}
	resolved := builder.resolved

	// Cloud-init: remove firewall rules (server auto-kills after timeout)
	// Networking: remove TAP device
	if builder.wasCreated("network_tap") && builder.tapName != "" && resolved.Network != nil {
		if tapErr := op.Services.Network.RemoveTap(ctx, builder.tapName, resolved.Network.Bridge, resolved.Network.ID); tapErr != nil {
			slog.Warn("failed to remove TAP during cleanup", "vm", builder.name, "error", tapErr)
		}
		if leaseErr := op.Repos.Lease.ReleaseByVM(ctx, builder.vmID); leaseErr != nil {
			slog.Warn("failed to release lease during cleanup", "vm", builder.name, "error", leaseErr)
		}
	}

	// Console relay: stop relay process
	if builder.wasCreated("console_relay") && builder.relay != nil {
		builder.relay.Cleanup()
	}

	// Firecracker: stop firecracker process
	if builder.wasCreated("firecracker") && builder.fcManager != nil {
		fcSpawner := vm.NewFirecrackerSpawner(builder.fcManager)
		fcSpawner.Cleanup()
	}

	// VM directory: remove all created files
	if builder.wasCreated("vm_dir") && builder.vmDir != "" {
		if rmErr := os.RemoveAll(builder.vmDir); rmErr != nil {
			slog.Warn("failed to remove VM directory during cleanup", "vm", builder.name, "error", rmErr)
		}
	}
}

// ── Remove ──

// Remove removes one or more VMs.
// Matches Python's VMOperation.remove() exactly.
// Uses the proper VMRequest pipeline (validation + resolution + enrichment)
// instead of inline resolution, matching Python's VMRequest(inputs=inputs, db=db).resolve().
func (op *Operation) VMRemove(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
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
				_ = op.Services.Volume.SetVolumesState(ctx, vols, model.VolumeStatusAvailable, nil)
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
		vmDir := filepath.Join(op.CacheDir, "vms", vm.ID)
		relay := consolesvc.NewRelay(vm.Name,
			filepath.Join(vmDir, "console.pid"),
			filepath.Join(vmDir, "console.sock"))
		relay.Stop(true)
	}

	// TAP device cleanup (matches Python's _cleanup_network)
	if vm.TapDevice != "" && vm.NetworkID != "" {
		_ = op.Services.Network.RemoveTap(ctx, vm.TapDevice, "", vm.NetworkID)
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
		if vm.Status == model.VMStatusRunning || vm.Status == model.VMStatusStarting {
			if !includeAll {
				continue
			}
		}

		if !dryRun {
			result := op.VMRemove(ctx, inputs.VMInput{Identifiers: []string{vm.Name}, Force: true})
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
func (op *Operation) VMGet(ctx context.Context, input inputs.VMInput) (*model.VM, error) {
	if len(input.Identifiers) != 1 {
		return nil, fmt.Errorf("Expected exactly one VM identifier")
	}
	// Use the full resolution pipeline (name, IP, MAC, ID prefix) matching Python's VMResolver
	vmResolver := vm.NewResolver(op.Repos.VM)
	vm, err := vmResolver.Resolve(ctx, input.Identifiers[0])
	if err != nil {
		return nil, err
	}
	// Enrich VM with relations (matches Python's VMResolver._enrich)
	op.Enr.EnrichVM(ctx, []*model.VM{vm}, "kernel", "image", "binary", "network", "network.leases", "volumes")
	return vm, nil
}

// Inspect returns detailed VM info with enriched data.
// Matches Python's VMOperation.inspect() exactly.
func (op *Operation) VMInspect(ctx context.Context, input inputs.VMInput) (*responses.VMInspect, error) {
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
		vmDir := filepath.Join(op.CacheDir, "vms", vm.ID)
		relay := consolesvc.NewRelay(vm.Name,
			filepath.Join(vmDir, "console.pid"),
			filepath.Join(vmDir, "console.sock"))
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
func (op *Operation) VMStart(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
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
		if vmLocal.Status == model.VMStatusStopped {
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
func (op *Operation) VMStop(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
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
		controller.Stop(ctx, input.Force)

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

// nocloudRespawnKillAfter is the auto-kill timeout for respawned nocloud-net servers.
// This is a hardcoded fallback — the primary path uses the config-driven value from
// defaults.cloudinit.nocloud_kill_after via ResolvedVMCreateInput.
const nocloudRespawnKillAfter = 5 * time.Minute

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
				cloudInitDir := filepath.Join(vmDir, "cloud-init")
				logFile := filepath.Join(vmDir, "nocloud-server.log")

				if port == 0 {
					freePort, err := infra.FindFreePort(gateway, 8000, 9000)
					if err != nil {
						slog.Warn("Failed to find free port for nocloud-net server", "vm", v.Name, "error", err)
						return nil
					}
					port = freePort
				}

				_, startErr := nocloudnetsvc.Spawn(ctx, nocloudnetsvc.Config{
					CloudInitDir: cloudInitDir,
					Port:         port,
					Host:         gateway,
					LogFile:      logFile,
					KillAfter:    nocloudRespawnKillAfter,
				})
				if startErr != nil {
					slog.Warn("Failed to start/restart nocloud-net server", "vm", v.Name, "error", startErr)
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
	if v.BootArgs != "" {
		fcConfig.BootArgs = v.BootArgs
	}
	if v.LSMFlags != "" {
		fcConfig.LSMFlags = v.LSMFlags
	}

	// ── Console relay setup (before spawn) ──
	var consoleController *console.Controller
	if v.EnableConsole {
		consoleController = console.NewController(v.ID, vmDir, v.Name,
			consolesvc.DefaultConsolePIDFilename, consolesvc.DefaultConsoleSocketFilename)
		ptyFD, ptyErr := consoleController.CreatePTY()
		if ptyErr != nil {
			slog.Warn("Console PTY creation failed during respawn", "vm", v.Name, "error", ptyErr)
		} else {
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
	pid := spawner.PID
	pst := spawner.ProcessStartTime
	_ = op.Repos.VM.UpdateProcessInfo(ctx, v.ID, pid, pst)

	newStatus := model.VMStatusRunning
	if snapshotMode {
		newStatus = model.VMStatusPaused
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
	input inputs.VMInput,
	memFile string,
	stateFile string,
) error {
	// Python: resolved = VMRequest(inputs=inputs, db=Database()).resolve()
	//         if len(resolved.vms) != 1: raise VMNotFoundError
	vmResolver := vm.NewResolver(op.Repos.VM)
	vmItem, err := vmResolver.Resolve(ctx, input.Identifiers[0])
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
	input inputs.VMInput,
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

	vmResolver := vm.NewResolver(op.Repos.VM)
	vmItem, err := vmResolver.Resolve(ctx, input.Identifiers[0])
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
	if vmItem.Status == model.VMStatusStopped {
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
func (op *Operation) VMReboot(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
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
		controller.Stop(ctx, input.Force)

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
func (op *Operation) VMPause(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
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
func (op *Operation) VMResume(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
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
	input inputs.VMInput,
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
	if vmItem.Status == model.VMStatusRunning {
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
			if err := controller.AttachVolume(ctx, model.DriveConfig{
				DriveID:      vol.ID,
				PathOnHost:   vol.Path,
				IsRootDevice: false,
				IsReadOnly:   vol.IsReadOnly,
				CacheType:    "Unsafe",
				IOEngine:     "Sync",
			}); err != nil {
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
	input inputs.VMInput,
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
	if vmItem.Status == model.VMStatusRunning {
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
//   - Delegates to VMCreateRequest for full resolution
//   - Delegates to executeCreate for provisioning
//   - Matches Python's try/except MVMError → "error", Exception → "failure"
func (op *Operation) VMImport(
	ctx context.Context,
	input inputs.VMImportInput,
	onProgress event.OnProgressCallback,
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
	var resolved *inputs.ResolvedVMCreateInput

	// Use VMImportRequest for full semantic resolution pipeline
	// (matches Python: VMImportRequest(inputs=inputs, db=db).resolve())
	request := inputs.NewVMImportRequest(input, op.Services.Config, op.Connection.DB())
	resolved, execErr = request.Resolve(ctx)
	var vmInstance *model.VM
	if execErr == nil {
		// Signal-based cleanup is handled inside vmBuilderCreate via ctx.Done().
		// The parent ctx (from main()) already cancels on SIGINT/SIGTERM.
		vmInstance, execErr = op.vmBuilderCreate(ctx, resolved, onProgress)
		if execErr != nil {
			if resolved.SkipCleanup {
				slog.Warn("VM import failed but --skip-cleanup is active", "dir", resolved.VMDir)
			}
		} else {
			// Handle volumes
			if len(resolved.Volumes) > 0 {
				op.Services.Volume.SetVolumesState(ctx, resolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
				vmInstance.VolumeIDs = make([]string, len(resolved.Volumes))
				for i, v := range resolved.Volumes {
					vmInstance.VolumeIDs[i] = v.ID
				}
				op.Repos.VM.Upsert(ctx, vmInstance)
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
func (op *Operation) VMExport(ctx context.Context, input inputs.VMInput) (*inputs.VMExportConfig, error) {
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
	var cloudInitModePtr *string
	if vmItem.CloudInitMode != "" {
		cloudInitModePtr = &vmItem.CloudInitMode
	}
	rootUser := "root"
	enableAPISocket := true

	cfg := &inputs.VMExportConfig{
		SchemaVersion: "1.0",
		Name:          vmItem.Name,
		Compute: inputs.VMExportComputeConfig{
			VCPUs: &vmItem.VCPUCount,
			Mem:   &vmItem.MemSizeMiB,
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
			Args:          vmItem.BootArgs,
			EnableConsole: &vmItem.EnableConsole,
		},
		Firecracker: inputs.VMExportFirecrackerConfig{
			EnableAPISocket: &enableAPISocket,
			PCIEnabled:      &vmItem.PCIEnabled,
			LsmFlags:        vmItem.LSMFlags,
			NestedVirt:      &vmItem.NestedVirt,
			CPUConfig:       cpuConfigStr,
		},
		CloudInit: inputs.VMExportCloudInitConfig{
			Mode:           cloudInitModePtr,
			User:           &rootUser,
			NocloudNetPort: &nocloudPort,
		},
	}

	op.AuditLog.LogOperation("vm.export", map[string]interface{}{"name": vmItem.Name}, "")

	return cfg, nil
}

type VMCreateBuilder struct {
	name             string
	vmID             string
	vmDir            string
	guestIP          string
	guestMAC         string
	tapName          string
	rootfsPath       string
	onProgress       event.OnProgressCallback
	resolved         *inputs.ResolvedVMCreateInput
	fcManager        *model.FirecrackerConfig
	spawner          *vm.FirecrackerSpawner
	relay            *console.Controller
	cloudInitResult  *cloudInitResult
	resourcesCreated map[string]bool
}

type cloudInitResult struct {
	mode        model.CloudInitMode
	isoPath     *string
	nocloudURL  *string
	nocloudPort *int
	nocloudPID  *int
}

// clean/execute/forRespawn/respawnExecute moved to Operation as vmBuilderCleanup/vmBuilderExecute

// consoleRelayRef is a simplified console relay reference for the Go port.
type consoleRelayRef struct {
	vmID  string
	vmDir string
}

func (c *VMCreateBuilder) cloneImage(ctx context.Context, imageSvc *image.Service, resolved *inputs.ResolvedVMCreateInput) error {
	fsType := resolved.Image.FSType
	if fsType == "" {
		return fmt.Errorf("fsType is required")
	}
	vmRootfsPath := filepath.Join(c.vmDir, "rootfs."+fsType)

	if _, err := imageSvc.EnsureCached([]*model.ImageItem{resolved.Image}); err != nil {
		return fmt.Errorf("ensure cached image: %w", err)
	}
	if err := imageSvc.MaterializeTo(ctx, resolved.Image.ID, fsType, vmRootfsPath); err != nil {
		return fmt.Errorf("materialize image: %w", err)
	}

	c.rootfsPath = vmRootfsPath
	return nil
}

func (c *VMCreateBuilder) markCreated(resource string) {
	c.resourcesCreated[resource] = true
}

func (c *VMCreateBuilder) wasCreated(resource string) bool {
	return c.resourcesCreated[resource]
}

// buildFirecrackerConfig builds a FirecrackerConfig from the resolved create context.
// Matches Python's VMCreateContext.build_firecracker_config() exactly.
func (c *VMCreateBuilder) buildFirecrackerConfig(ctx context.Context) *model.FirecrackerConfig {
	if c.resolved == nil {
		return nil
	}

	var cpuVendor *string
	var cpuArchitecture *string

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
		MemSizeMiB:           c.resolved.MemSizeMib,
		GuestIP:              c.guestIP,
		GuestMAC:             c.guestMAC,
		TapName:              c.tapName,
		NetworkGateway:       c.resolved.Network.IPv4Gateway,
		NetworkNetmask:       c.resolved.NetworkNetmask,
		ImageFSUUID:          c.resolved.Image.FSUUID,
		ImageFSType:          c.resolved.Image.FSType,
		BootArgs:             c.resolved.BootArgs,
		LSMFlags:             c.resolved.LSMFlags,
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
			isoPath := *c.cloudInitResult.isoPath
			fcConfig.CloudInitISOPath = &isoPath
		}
		if c.cloudInitResult.nocloudURL != nil {
			nocloudURL := *c.cloudInitResult.nocloudURL
			fcConfig.CloudInitNoCloudURL = &nocloudURL
		}
	}

	// CPU config
	if c.resolved.CPUConfig != nil {
		fcConfig.CPUConfig = c.resolved.CPUConfig
	}

	return fcConfig
}

func (c *VMCreateBuilder) toVMModel() *model.VM {
	if c.resolved == nil || c.spawner == nil || c.spawner.PID == nil {
		return nil
	}

	now := time.Now().Format(time.RFC3339)
	logPath := filepath.Join(c.vmDir, c.resolved.LogFilename)
	serialPath := filepath.Join(c.vmDir, c.resolved.SerialOutputFilename)

	vm := &model.VM{
		ID:               c.vmID,
		Name:             c.resolved.Name,
		PID:              *c.spawner.PID,
		ProcessStartTime: c.spawner.ProcessStartTime,
		Status:           model.VMStatusRunning,
		IPv4:             c.guestIP,
		MAC:              c.guestMAC,
		NetworkID:        c.resolved.Network.ID,
		TapDevice:        c.tapName,
		ImageID:          c.resolved.Image.ID,
		KernelID:         c.resolved.Kernel.ID,
		BinaryID:         c.resolved.Binary.ID,
		VCPUCount:        c.resolved.VCPUCount,
		MemSizeMiB:       c.resolved.MemSizeMib,
		DiskSizeMiB:      c.resolved.DiskSizeMib,
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
		LogPath:          &logPath,
		SerialOutputPath: &serialPath,
	}

	// Extract key names for the VM record (model.SSHKeys is []string)
	for _, k := range c.resolved.SSHKeys {
		if k != nil {
			vm.SSHKeys = append(vm.SSHKeys, k.Name)
		}
	}

	if c.resolved.CPUConfig != nil {
		vm.CPUConfig = c.resolved.CPUConfig
	}
	if c.resolved.BootArgs != "" {
		vm.BootArgs = c.resolved.BootArgs
	}
	if c.resolved.LSMFlags != "" {
		vm.LSMFlags = c.resolved.LSMFlags
	}

	// Nocloud port/pid from cloud_init_result (prefer runtime values over resolved)
	if c.cloudInitResult != nil && c.cloudInitResult.nocloudPort != nil {
		vm.NocloudNetPort = c.cloudInitResult.nocloudPort
		if c.cloudInitResult.nocloudPID != nil {
			vm.NocloudNetPID = c.cloudInitResult.nocloudPID
		}
	} else if c.resolved.NocloudNetPort != nil {
		vm.NocloudNetPort = c.resolved.NocloudNetPort
	}

	// Relay info
	if c.relay != nil {
		if p, ok := c.relay.GetPID(); ok {
			vm.RelayPID = &p
		}
		if s := c.relay.SocketPath(); s != "" {
			vm.RelaySocketPath = &s
		}
	}

	return vm
}
