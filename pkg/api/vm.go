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
	infraslice "mvmctl/internal/infra/slice"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
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

	// Network bridge setup (shared across all VMs in the batch, done once).
	bridgeAddr, calcErr := network.ComputeBridgeAddress(
		sharedResolved.Network.IPv4Gateway, sharedResolved.Network.Subnet,
	)
	if calcErr != nil {
		return nil, fmt.Errorf("compute bridge address: %w", calcErr)
	}
	// Bridge + NAT in a single batch for atomic network and firewall rule application.
	natGateways := network.NatGatewaysList(sharedResolved.Network)
	var bridgeErr error
	op.Services.Network.WithBatch(ctx, func() {
		if err := op.Services.Network.EnsureBridge(ctx, sharedResolved.Network.Bridge, bridgeAddr); err != nil {
			bridgeErr = err
			return
		}
		if sharedResolved.Network.NATEnabled && len(natGateways) > 0 {
			if natErr := op.Services.Network.EnsureNAT(ctx, sharedResolved.Network.Bridge,
				natGateways, sharedResolved.Network.Subnet, sharedResolved.Network.ID); natErr != nil {
				slog.Warn("failed to ensure NAT rules during VM creation",
					"vm", "(shared)", "error", natErr)
			}
		}
	})
	if bridgeErr != nil {
		return nil, fmt.Errorf("ensure bridge: %w", bridgeErr)
	}

	// Shared nocloudnet server for NET mode (across all VMs in the batch).
	if sharedResolved.CloudInitMode == model.CloudInitModeNET {
		// Create shared batch directory.
		batchID := crypto.BatchID(input.Name, time.Now().Format(time.RFC3339))
		nocloudDir := infra.GetNoCloudNetBatchDir(batchID)
		if err := os.MkdirAll(nocloudDir, 0755); err != nil {
			return nil, fmt.Errorf("create nocloud batch directory: %w", err)
		}

		// Find a free port for the nocloud server.
		port := sharedResolved.NocloudNetPort
		var freePort int
		if port != nil && *port > 0 {
			freePort = *port
		} else {
			p, err := infra.FindFreePort(
				sharedResolved.Network.IPv4Gateway,
				sharedResolved.NocloudPortRangeStart,
				sharedResolved.NocloudPortRangeEnd,
			)
			if err != nil {
				return nil, fmt.Errorf("find free port for nocloud server: %w", err)
			}
			freePort = p
		}

		// Spawn ONE nocloud server for the entire batch.
		nocloudLog := infra.GetNoCloudNetLogPath(batchID)
		result, err := nocloudnetsvc.Spawn(ctx, nocloudnetsvc.Config{
			BaseDir:   nocloudDir,
			Port:      freePort,
			Host:      sharedResolved.Network.IPv4Gateway,
			LogFile:   nocloudLog,
			KillAfter: sharedResolved.NoCloudKillAfter,
		})
		if err != nil {
			return nil, fmt.Errorf("spawn nocloud server: %w", err)
		}

		sharedResolved.NoCloudURL = result.URL
		sharedResolved.NoCloudPort = result.Port
		sharedResolved.NoCloudPID = result.PID
		sharedResolved.NoCloudSharedDir = nocloudDir
	}

	// Parallel VM creation: one goroutine per VM.
	batchCtx, batchCancel := context.WithCancel(ctx)
	defer batchCancel()

	type vmResult struct {
		idx int
		vm  *model.VM
		err error
	}
	resultCh := make(chan vmResult, len(names))

	for idx, name := range names {
		go func(idx int, name string) {
			createdAt := time.Now()
			vmID := crypto.VMID(name, createdAt.Format(time.RFC3339))
			vmDir := infra.GetVMDirByID(vmID)

			resolved := request.CloneVMInput(sharedResolved, name, vmID, vmDir)

			// Progress wrapper (idx, name captured by value via closure param).
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

			vmInstance, execErr := op.vmBuilderCreate(batchCtx, resolved, progress)
			if execErr != nil {
				resultCh <- vmResult{idx: idx, err: execErr}
				return
			}

			// Handle volumes (per VM, inside goroutine for independence).
			if resolved.Volumes != nil && len(resolved.Volumes) > 0 {
				op.Services.Volume.SetVolumesState(ctx, resolved.Volumes, model.VolumeStatusAttached, &vmInstance.ID)
				vmInstance.VolumeIDs = make([]string, len(resolved.Volumes))
				for i, v := range resolved.Volumes {
					vmInstance.VolumeIDs[i] = v.ID
				}
				op.Repos.VM.Upsert(ctx, vmInstance)
			}

			// Audit log per VM.
			op.AuditLog.LogOperation("vm.create", nil, fmt.Sprintf("name=%s", name))

			resultCh <- vmResult{idx: idx, vm: vmInstance}
		}(idx, name)
	}

	// Collect results. On first error in atomic mode, cancel remaining goroutines.
	createdVMs := make([]*model.VM, 0, len(names))
	var createErrors []string
	atomicFailed := false

	for range names {
		res := <-resultCh
		if res.err != nil {
			createErrors = append(createErrors, fmt.Sprintf("%s: %v", names[res.idx], res.err))
			if input.Atomic {
				atomicFailed = true
				batchCancel()
			}
			continue
		}
		createdVMs = append(createdVMs, res.vm)
	}

	// Atomic rollback: remove all successfully created VMs.
	if atomicFailed && len(createdVMs) > 0 {
		for _, vm := range createdVMs {
			_ = op.VMRemove(ctx, inputs.VMInput{Identifiers: []string{vm.Name}, Force: true})
		}
		return nil, &errs.DomainError{
			Code: "vm.atomic_failed",
			Op:   "vm",
			Message: fmt.Sprintf(
				"Atomic creation failed: %s. All %d created VMs have been removed.",
				strings.Join(createErrors, "; "),
				len(createdVMs),
			),
			Class: errs.ClassInternal,
		}
	}

	if len(createErrors) > 0 && len(createdVMs) == 0 {
		return nil, &errs.DomainError{
			Code:    "vm.create_failure",
			Op:      "vm",
			Message: strings.Join(createErrors, "; "),
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
func (op *Operation) vmBuilderExecute(
	ctx context.Context,
	builder *VMCreateBuilder,
	resolved *inputs.ResolvedVMCreateInput,
) error {
	if resolved == nil {
		return fmt.Errorf("failed to resolve necessary dependencies")
	}

	// Validate socket path before any expensive operations (cloud-init, resize, etc.).
	if apiSocketPath := filepath.Join(builder.vmDir, resolved.APISocketFilename); len(apiSocketPath) >= 108 {
		return fmt.Errorf(
			"VM ID '%s' produces a socket path that is too long (%d chars, max 107). This is a system limit for Unix domain sockets. Path: %s",
			builder.vmID,
			len(apiSocketPath),
			apiSocketPath,
		)
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

	// TAP setup (NAT rules are done once before the per-VM loop).
	var tapErr error
	op.Services.Network.WithBatch(ctx, func() {
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
		// Determine the cloud-init directory. For NET mode with a shared batch
		// nocloud server, use a per-VM subdirectory under the shared batch dir.
		cloudInitDir := filepath.Join(builder.vmDir, "cloud-init")
		nocloudURL := resolved.NoCloudURL
		if resolved.CloudInitMode == model.CloudInitModeNET && resolved.NoCloudSharedDir != "" {
			cloudInitDir = filepath.Join(resolved.NoCloudSharedDir, builder.guestIP)
			nocloudURL = fmt.Sprintf("http://%s:%d/%s/",
				resolved.Network.IPv4Gateway, resolved.NoCloudPort, builder.guestIP)
		}
		ciConfig := &cloudinit.Config{
			Mode:                  resolved.CloudInitMode,
			VMName:                resolved.Name,
			VMID:                  builder.vmID,
			VMDir:                 builder.vmDir,
			CloudInitDir:          cloudInitDir,
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
			// Pre-allocated server (shared port, per-VM URL)
			NoCloudURL:  nocloudURL,
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
		if tapErr := op.Services.Network.RemoveTap(
			ctx,
			builder.tapName,
			resolved.Network.Bridge,
			resolved.Network.ID,
		); tapErr != nil {
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

	for _, vmLocal := range resolved.VMs {
		vmDir := infra.GetVMDirByID(vmLocal.ID)

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

		// Console relay, TAP, IP lease cleanup
		if vmLocal.RelayPID != nil {
			handler := system.NewProcessSignalHandler(system.ProcessSignalHandlerConfig{PID: *vmLocal.RelayPID})
			handler.GracefulShutdown(nil)
		}
		if vmLocal.RelaySocketPath != nil {
			_ = os.Remove(*vmLocal.RelaySocketPath)
		}
		if vmLocal.TapDevice != "" && vmLocal.NetworkID != "" {
			_ = op.Services.Network.RemoveTap(ctx, vmLocal.TapDevice, "", vmLocal.NetworkID)
		}
		if vmLocal.ID != "" {
			leaseRepo := network.NewLeaseRepository(op.Connection.DB())
			_ = leaseRepo.ReleaseByVM(ctx, vmLocal.ID)
		}

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

		op.AuditLog.LogOperation("vm.remove", map[string]any{"name": vmLocal.Name}, "")

		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.removed",
			Item: vmLocal, Message: fmt.Sprintf("VM '%s' removed", vmLocal.Name),
		})
	}

	return &errs.BatchResult{Items: results}
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
func (op *Operation) VMList(ctx context.Context, statuses ...string) []*model.VM {
	var vms []*model.VM
	var err error

	if len(statuses) > 0 {
		vms, err = op.Repos.VM.ListByStatus(ctx, statuses...)
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

	// Enrich all relations at once instead of manual repo calls.
	if err := op.Enr.EnrichVM(ctx, []*model.VM{vm},
		"kernel", "image", "binary", "network", "network.leases", "volumes",
	); err != nil {
		return nil, err
	}

	relayRunning := vm.RelayPID != nil && system.IsProcessRunning(*vm.RelayPID)

	vmDir := infra.GetVMDirByID(vm.ID)

	// Volumes (enriched vm.Volumes or fallback to manual lookup).
	var volumes []responses.VMVolume
	srcVols := vm.Volumes
	if len(srcVols) == 0 && len(vm.VolumeIDs) > 0 {
		vols, err := op.Repos.Volume.FindByIDs(ctx, vm.VolumeIDs)
		if err == nil {
			srcVols = vols
		}
	}
	for _, v := range srcVols {
		volumes = append(volumes, responses.VMVolume{
			ID: v.ID, Name: v.Name, Size: v.SizeBytes,
			Format: string(v.Format), Status: string(v.Status),
		})
	}

	configPath := &vm.ConfigPath
	if vm.ConfigPath == "" {
		configPath = nil
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
			IPv4: vm.IPv4, MAC: vm.MAC,
			Network: vm.Network, TapDevice: vm.TapDevice,
		},
		Assets: responses.VMAssetsInfo{
			Image:  vm.Image,
			Kernel: vm.Kernel,
			Binary: vm.Binary,
		},
		Filesystem: responses.VMFilesystemInfo{
			VMDir: vmDir, RootfsPath: vm.RootfsPath,
			ConfigPath:       configPath,
			LogPath:          vm.LogPath,
			SerialOutputPath: vm.SerialOutputPath,
		},
		Console: responses.VMConsoleInfo{
			RelayRunning: relayRunning, RelayPID: vm.RelayPID,
			RelaySocketPath: vm.RelaySocketPath,
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

	for _, vmLocal := range resolved.VMs {

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

	for _, vmLocal := range resolved.VMs {

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

func (op *Operation) vmRespawnFirecracker(ctx context.Context, v *model.VM, snapshotMode bool) error {
	vmDir := infra.GetVMDirByID(v.ID)

	// Network info is required — the VM record must have it pre-loaded.
	if v.Network == nil {
		return fmt.Errorf("network info is required for VM respawn")
	}
	if v.Binary == nil || v.Binary.Path == "" {
		return fmt.Errorf("binary info is required for VM respawn")
	}
	if v.KernelID == "" || v.Kernel == nil || v.Kernel.Path == "" {
		return fmt.Errorf("kernel info is required for VM respawn")
	}
	if v.RootfsPath == "" {
		return fmt.Errorf("rootfs path is required for VM respawn")
	}

	// ── Force-kill any remaining Firecracker process ──
	if v.PID > 0 && system.IsProcessRunning(v.PID) {
		handler := system.NewProcessSignalHandler(system.ProcessSignalHandlerConfig{
			PID:             v.PID,
			GracefulTimeout: 100 * time.Millisecond,
			KillTimeout:     50 * time.Millisecond,
		})
		handler.GracefulShutdown(nil)
	}

	// ── Batch: bridge, NAT, TAP, then ARP flush ──
	if v.TapDevice != "" && v.Network.Bridge != "" {
		bridgeAddr, calcErr := network.ComputeBridgeAddress(v.Network.IPv4Gateway, v.Network.Subnet)
		if calcErr != nil {
			slog.Warn("Failed to compute bridge address during respawn", "vm", v.Name, "error", calcErr)
		} else {
			op.Services.Network.WithBatch(ctx, func() {
				if err := op.Services.Network.EnsureBridge(ctx, v.Network.Bridge, bridgeAddr); err != nil {
					slog.Warn("Failed to ensure bridge during respawn", "bridge", v.Network.Bridge, "error", err)
					return
				}
				if v.Network.NATEnabled {
					natGateways := network.NatGatewaysList(v.Network)
					if len(natGateways) > 0 {
						if err := op.Services.Network.EnsureNAT(ctx, v.Network.Bridge,
							natGateways, v.Network.Subnet, v.Network.ID); err != nil {
							slog.Warn("Failed to ensure NAT rules during respawn",
								"vm", v.Name, "error", err)
						}
					}
				}
				if err := op.Services.Network.EnsureTap(
					ctx,
					v.TapDevice,
					v.Network.Bridge,
					v.Network.ID,
					v.Network.Subnet,
				); err != nil {
					slog.Warn("Failed to ensure TAP during respawn", "tap", v.TapDevice, "error", err)
				}
			})
		}
		infranet.FlushARP(ctx, v.Network.Bridge)
	}

	fcConfig := &model.FirecrackerConfig{
		VMDir:                vmDir,
		RootfsPath:           v.RootfsPath,
		BinaryPath:           v.Binary.Path,
		KernelPath:           v.Kernel.Path,
		VCPUCount:            v.VCPUCount,
		MemSizeMiB:           v.MemSizeMiB,
		GuestIP:              v.IPv4,
		GuestMAC:             v.MAC,
		TapName:              v.TapDevice,
		NetworkGateway:       v.Network.IPv4Gateway,
		PCIEnabled:           v.PCIEnabled,
		NestedVirt:           v.NestedVirt,
		EnableConsole:        v.EnableConsole,
		EnableLogging:        v.EnableLogging,
		EnableMetrics:        v.EnableMetrics,
		LogLevel:             v.LogLevel,
		LogFilename:          v.LogFilename,
		SerialOutputFilename: v.SerialOutputFilename,
		MetricsFilename:      v.MetricsFilename,
		APISocketFilename:    v.APISocketFilename,
		PIDFilename:          v.PIDFilename,
		ConfigFilename:       v.ConfigFilename,
		BootArgs:             v.BootArgs,
		LSMFlags:             v.LSMFlags,
		SnapshotMode:         snapshotMode,
	}

	// ── Console relay setup (before spawn) ──
	var consoleController *console.Controller
	if v.EnableConsole {
		consoleController = console.NewController(v.ID, vmDir, v.Name,
			v.ConsolePIDFilename, v.ConsoleSocketFilename)
		ptyFD, ptyErr := consoleController.CreatePTY()
		if ptyErr != nil {
			slog.Warn("Console PTY creation failed during respawn", "vm", v.Name, "error", ptyErr)
		} else {
			fcConfig.RelayClientFD = &ptyFD
		}
	}

	// ── Write config and spawn ──
	spawner := vm.NewFirecrackerSpawner(fcConfig)
	if err := spawner.WriteToFile(); err != nil {
		return fmt.Errorf("write firecracker config: %w", err)
	}
	if err := spawner.Spawn(); err != nil {
		slog.Warn("Failed to respawn Firecracker", "vm", v.Name, "error", err)
		return err
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

	// ── Update DB and in-memory VM object ──
	pid := spawner.PID
	pst := spawner.ProcessStartTime
	if err := op.Repos.VM.UpdateProcessInfo(ctx, v.ID, pid, pst); err != nil {
		slog.Warn("Failed to update VM process info", "vm", v.Name, "error", err)
	}

	newStatus := model.VMStatusRunning
	if snapshotMode {
		newStatus = model.VMStatusPaused
	}
	if err := op.Repos.VM.UpdateStatus(ctx, v.ID, newStatus); err != nil {
		slog.Warn("Failed to update VM status", "vm", v.Name, "error", err)
	}

	if pid != nil {
		v.PID = *pid
	} else {
		v.PID = 0
	}
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

	for _, vmLocal := range resolved.VMs {

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

	for _, vmLocal := range resolved.VMs {

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

	for _, vmLocal := range resolved.VMs {

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
				if !version.IsAtLeastFor(bin.Version, version.FeatureHotplug) {
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
				if !version.IsAtLeastFor(bin.Version, version.FeatureHotUnplug) {
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
		return nil, fmt.Errorf("vm not found: %w", err)
	}

	// Resolve related asset metadata (matches Python's Repository(db).get(vm.image_id) etc.)
	image, err := op.Repos.Image.Get(ctx, vmItem.ImageID)
	if err != nil {
		slog.Warn("failed to resolve image for export", "vm", vmItem.Name, "image_id", vmItem.ImageID, "error", err)
	}
	kernel, err := op.Repos.Kernel.Get(ctx, vmItem.KernelID)
	if err != nil {
		slog.Warn("failed to resolve kernel for export", "vm", vmItem.Name, "kernel_id", vmItem.KernelID, "error", err)
	}
	binary, err := op.Repos.Binary.Get(ctx, vmItem.BinaryID)
	if err != nil {
		slog.Warn("failed to resolve binary for export", "vm", vmItem.Name, "binary_id", vmItem.BinaryID, "error", err)
	}
	netItem, err := op.Repos.Network.Get(ctx, vmItem.NetworkID)
	if err != nil {
		slog.Warn(
			"failed to resolve network for export",
			"vm",
			vmItem.Name,
			"network_id",
			vmItem.NetworkID,
			"error",
			err,
		)
	}

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

	// Resolve values from related assets (nil-safe: if asset not found, fields stay empty)
	imageType := ""
	imageArch := ""
	if image != nil {
		imageType = image.Type
		imageArch = image.Arch
	}

	kernelVersion := ""
	kernelArch := ""
	kernelType := ""
	if kernel != nil {
		kernelVersion = kernel.Version
		kernelArch = kernel.Arch
		kernelType = kernel.Type
	}

	netName := ""
	netSubnet := ""
	netGateway := ""
	netNATGateways := ""
	var netNATEnabled *bool
	if netItem != nil {
		netName = netItem.Name
		netSubnet = netItem.Subnet
		netGateway = netItem.IPv4Gateway
		gws := network.NatGatewaysList(netItem)
		netNATGateways = strings.Join(gws, ",")
		netNATEnabled = &netItem.NATEnabled
	}

	// Convert cpu_config to JSON string (matches Python: json.dumps(vm.cpu_config) if isinstance(vm.cpu_config, dict) else vm.cpu_config)
	cpuConfigStr := ""
	if vmItem.CPUConfig != nil {
		if data, err := json.Marshal(vmItem.CPUConfig); err == nil {
			cpuConfigStr = string(data)
		}
	}

	rootUser := "root"
	enableAPISocket := true

	cfg := &inputs.VMExportConfig{
		SchemaVersion: "1.0",
		Name:          vmItem.Name,
		Compute: inputs.VMExportComputeConfig{
			VCPUs: vmItem.VCPUCount,
			Mem:   vmItem.MemSizeMiB,
		},
		Image: inputs.VMExportImageConfig{
			Type:     imageType,
			Arch:     imageArch,
			DiskSize: diskSize,
		},
		Kernel: inputs.VMExportKernelConfig{
			Version: kernelVersion,
			Arch:    kernelArch,
			Type:    kernelType,
		},
		Binary: inputs.VMExportBinaryConfig{
			Name:    binName,
			Version: binVersion,
		},
		Network: inputs.VMExportNetworkConfig{
			Name:        netName,
			Subnet:      netSubnet,
			IPv4Gateway: netGateway,
			NATGateways: netNATGateways,
			NATEnabled:  netNATEnabled,
			IP:          vmItem.IPv4,
			MAC:         vmItem.MAC,
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
			Mode:           vmItem.CloudInitMode,
			User:           rootUser,
			NocloudNetPort: nocloudPort,
		},
	}

	op.AuditLog.LogOperation("vm.export", map[string]any{"name": vmItem.Name}, "")

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

func (c *VMCreateBuilder) cloneImage(
	ctx context.Context,
	imageSvc *image.Service,
	resolved *inputs.ResolvedVMCreateInput,
) error {
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
