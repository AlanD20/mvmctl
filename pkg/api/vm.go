// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/internal/core/cloudinit"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/infra/timinglog"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	libnet "mvmctl/internal/lib/network"
	"mvmctl/internal/lib/provisioner"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/service/jailer"
	networkservice "mvmctl/internal/service/network"
	nocloudnetsvc "mvmctl/internal/service/nocloudnet"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// VMAPI defines the public interface for VM operations.
type VMAPI interface {
	VMCreate(
		ctx context.Context,
		input inputs.VMCreateInput,
		onProgress event.OnProgressCallback,
	) ([]*model.VMItem, error)
	VMRemove(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	VMList(ctx context.Context, statuses ...string) ([]*model.VMItem, error)
	VMGet(ctx context.Context, input inputs.VMInput) (*model.VMItem, error)
	VMInspect(ctx context.Context, input inputs.VMInput) (*results.VMInspect, error)
	VMStart(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMStop(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMReboot(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMPause(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMResume(ctx context.Context, input inputs.VMInput) *errs.BatchResult
}

// --- Create ---
// Create creates one or more VMs.
func (op *Operation) VMCreate(
	ctx context.Context,
	input inputs.VMCreateInput,
	onProgress event.OnProgressCallback,
) ([]*model.VMItem, error) {
	count := 1
	if input.Count != nil && *input.Count > 1 {
		count = *input.Count
	}
	names := vm.GenerateBatchNames(input.Name, count)
	// Pre-allocate: check name collisions (single query)
	existing, err := op.Repos.VM.NamesExist(ctx, names)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, fmt.Sprintf("Failed to check name collisions: %v", err), err)
	}
	if len(existing) > 0 {
		return nil, errs.New(
			errs.CodeVMNameCollision,
			fmt.Sprintf("VM name(s) already exist: %s", strings.Join(existing, ", ")),
		)
	}
	// Resolve shared state ONCE before the loop.
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
	pair, err := op.Services.Binary.ResolvePair(ctx, sharedResolved.Binary)
	if err != nil {
		return nil, err
	}
	sharedResolved.Binary = pair.Firecracker
	sharedResolved.Jailer = pair.Jailer
	sharedResolved.Provisioner = model.ProvisionerType(op.ProvisionerType)
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
		vm  *model.VMItem
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
	createdVMs := make([]*model.VMItem, 0, len(names))
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
		return nil, errs.New(
			errs.CodeVMAtomicFailed,
			fmt.Sprintf(
				"Atomic creation failed: %s. All %d created VMs have been removed.",
				strings.Join(createErrors, "; "),
				len(createdVMs),
			),
		)
	}
	if len(createErrors) > 0 && len(createdVMs) == 0 {
		return nil, errs.New(errs.CodeVMCreateFailure, strings.Join(createErrors, "; "))
	}
	return createdVMs, nil
}
func (op *Operation) vmBuilderCreate(
	ctx context.Context,
	resolved *inputs.ResolvedVMCreateInput,
	onProgress event.OnProgressCallback,
) (*model.VMItem, error) {
	if resolved == nil {
		return nil, fmt.Errorf("failed to resolve necessary dependencies")
	}
	if resolved.VMDir == "" {
		return nil, fmt.Errorf("vm directory not set")
	}
	// Create data holder for creation state
	builder := &VMCreateBuilder{
		cacheRoot:        op.CacheDir,
		name:             resolved.Name,
		vmID:             resolved.VMID,
		vmDir:            resolved.VMDir,
		onProgress:       onProgress,
		resolved:         resolved,
		resourcesCreated: make(map[string]bool),
	}
	// Cleanup begins only after execution returns so it cannot race a launch.
	var vmInstance *model.VMItem
	execErr := op.vmBuilderExecute(ctx, builder, resolved)
	if execErr == nil {
		vmInstance = builder.toVMModel()
		if vmInstance == nil {
			if builder.spawner == nil {
				execErr = fmt.Errorf("firecracker spawner is not set in context")
			} else if builder.spawner.PID == nil {
				execErr = fmt.Errorf("failed to spawn firecracker process")
			} else {
				execErr = fmt.Errorf("failed to create VM instance model")
			}
		} else if err := op.Repos.VM.Upsert(ctx, vmInstance); err != nil {
			execErr = fmt.Errorf("upsert VM: %w", err)
		} else if builder.vsockCID > 0 && builder.vsockToken != "" {
			if err := op.Services.Vsock.PersistConfig(ctx,
				builder.vsockCID, builder.vmID, builder.name,
				builder.vsockUDSPath, builder.vsockPort, builder.vsockToken,
			); err != nil {
				execErr = fmt.Errorf("persist vsock config: %w", err)
			}
		}
	}
	if execErr != nil {
		if !resolved.SkipCleanup {
			execErr = errors.Join(execErr, op.vmBuilderCleanup(context.WithoutCancel(ctx), builder))
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
	if apiSocketPath := vm.RuntimePath(os.Geteuid(), builder.vmID, infra.VMFirecrackerAPISocketFilename); len(apiSocketPath) >= 108 {
		return fmt.Errorf(
			"VM ID '%s' produces a socket path that is too long (%d chars, max 107). "+
				"This is a system limit for Unix domain sockets. Path: %s",
			builder.vmID,
			len(apiSocketPath),
			apiSocketPath,
		)
	}
	tl := timinglog.Start("vm_create", "vm_name", builder.name, "vm_id", builder.vmID)
	defer tl.Complete()

	// Generate MAC and TAP name
	if resolved.RequestedGuestMAC != nil {
		builder.guestMAC = *resolved.RequestedGuestMAC
	} else {
		builder.guestMAC = libnet.VMGenerateMAC(resolved.GuestMACPrefix)
	}
	tapName, err := networkservice.VMTAPName(uint32(os.Geteuid()), resolved.Network.ID, builder.vmID)
	if err != nil {
		return err
	}
	builder.tapName = tapName
	// The receiver admits only private caller-owned managed directories.
	if err := os.MkdirAll(filepath.Dir(builder.vmDir), 0700); err != nil {
		return fmt.Errorf("create managed VM parent directory: %w", err)
	}
	// Create VM directory
	if err := os.MkdirAll(builder.vmDir, 0700); err != nil {
		return fmt.Errorf("create vm directory: %w", err)
	}
	builder.markCreated("vm_dir")
	// Progress: network
	emitProgress(builder.onProgress, "network", "running", "Configuring network...")
	// IP Lease, TAP device creation (timed)
	var networkErr error
	tl.StageFunc("network", func() {
		leaseCtrl, err := network.NewLeaseController(ctx, resolved.Network, op.Repos.Lease, nil)
		if err != nil {
			networkErr = fmt.Errorf("create lease controller: %w", err)
			return
		}
		if resolved.RequestedGuestIP != nil {
			ip, leaseErr := leaseCtrl.LeaseSpecific(ctx, *resolved.RequestedGuestIP, builder.vmID)
			if leaseErr != nil {
				networkErr = fmt.Errorf("lease specific ip: %w", leaseErr)
				return
			}
			builder.guestIP = ip
		} else {
			ip, leaseErr := leaseCtrl.Lease(ctx, builder.vmID)
			if leaseErr != nil {
				networkErr = fmt.Errorf("lease ip: %w", leaseErr)
				return
			}
			builder.guestIP = ip
		}
	})
	if networkErr != nil {
		return networkErr
	}
	builder.markCreated("network_lease")
	// Progress: rootfs
	emitProgress(builder.onProgress, "rootfs", "running", "Copying root filesystem...")
	// Clone rootfs (timed)
	var cloneErr error
	tl.StageFunc("clone_rootfs", func() {
		if err := builder.cloneImage(ctx, op.Services.Image, resolved); err != nil {
			cloneErr = err
		}
	})
	if cloneErr != nil {
		return cloneErr
	}
	builder.markCreated("rootfs")
	// Progress: cloud-init
	emitProgress(builder.onProgress, "cloud-init", "running", "Provisioning cloud-init...")
	// Cloud-init provisioning, rootfs operations (timed)
	var (
		provisionErr    error
		cloudInitMarker string
		ciResultOut     *cloudInitResult
	)
	tl.StageFunc("provision_rootfs", func() {
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
			provisionErr = fmt.Errorf("failed to create VM provisioner: %w", backendErr)
			return
		}
		// Resize rootfs
		if resizeErr := backend.Resize(ctx, resolved.DiskSizeBytes); resizeErr != nil {
			provisionErr = fmt.Errorf("resize rootfs: %w", resizeErr)
			return
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
				provisionErr = fmt.Errorf("set hostname: %w", err)
				return
			}
			if err := backend.InjectDNS(ctx, resolved.DNSServer); err != nil {
				provisionErr = fmt.Errorf("inject DNS: %w", err)
				return
			}
			if err := backend.SetupSSH(ctx, resolved.User, pubkeys); err != nil {
				provisionErr = fmt.Errorf("setup SSH: %w", err)
				return
			}
			if err := backend.SetupSudo(ctx, resolved.User); err != nil {
				provisionErr = fmt.Errorf("setup sudo: %w", err)
				return
			}
		}
		if resolved.CloudInitMode == model.CloudInitModeOFF {
			if err := backend.DisableCloudInit(ctx); err != nil {
				provisionErr = fmt.Errorf("disable cloud-init: %w", err)
				return
			}
			cloudInitMarker = "cloud-init-off"
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
				NetworkID:             resolved.Network.ID,
				NetworkName:           resolved.Network.Name,
				IPv4Gateway:           resolved.Network.IPv4Gateway,
				NetworkPrefixLen:      resolved.NetworkPrefixLen,
				SkipNetworkConfig:     resolved.SkipCINetworkConfig,
				SSHPubkeys:            pubkeys,
				UserPassword:          userPassword,
				CustomCloudInitConfig: resolved.CustomCloudInitConfig,
				NocloudNetPort:        resolved.NocloudNetPort,
				CloudInitISOPath:      resolved.CloudInitISOPath,
				KeepCloudInitISO:      resolved.KeepCloudInitISO,
				NocloudPortRangeStart: resolved.NocloudPortRangeStart,
				NocloudPortRangeEnd:   resolved.NocloudPortRangeEnd,
				NocloudMaxPortRetries: resolved.NocloudMaxPortRetries,
			}
			ciProvisioner := cloudinit.NewProvisioner(ciConfig, op.Services.Network.FirewallTracker())
			ciResult, ciErr := ciProvisioner.Provision(ctx)
			if ciErr != nil {
				provisionErr = fmt.Errorf("cloud-init inject provisioning failed: %w", ciErr)
				return
			}
			ciResultOut = &cloudInitResult{
				mode: ciResult.Mode,
			}
			if err := backend.InjectCloudInit(ctx, ciConfig.CloudInitDir); err != nil {
				provisionErr = fmt.Errorf("inject cloud-init: %w", err)
				return
			}
			cloudInitMarker = "cloud-init-inject"
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
				NetworkID:             resolved.Network.ID,
				NetworkName:           resolved.Network.Name,
				IPv4Gateway:           resolved.Network.IPv4Gateway,
				NetworkPrefixLen:      resolved.NetworkPrefixLen,
				SkipNetworkConfig:     resolved.SkipCINetworkConfig,
				SSHPubkeys:            pubkeys,
				UserPassword:          userPassword,
				CustomCloudInitConfig: resolved.CustomCloudInitConfig,
				NocloudNetPort:        resolved.NocloudNetPort,
				CloudInitISOPath:      resolved.CloudInitISOPath,
				KeepCloudInitISO:      resolved.KeepCloudInitISO,
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
				provisionErr = fmt.Errorf("cloud-init provisioning failed: %w", ciErr)
				return
			}
			ciResultOut = &cloudInitResult{
				mode:        ciResult.Mode,
				isoPath:     ciResult.ISOPath,
				nocloudURL:  ciResult.NocloudURL,
				nocloudPort: &ciResult.NocloudPort,
				nocloudPID:  ciResult.NocloudPID,
			}
			if resolved.CloudInitMode == model.CloudInitModeISO {
				cloudInitMarker = "cloud-init-iso"
			} else {
				cloudInitMarker = "cloud-init-net"
			}
		}
		// Deblob (OS cache cleanup) unless explicitly skipped
		if !resolved.SkipDeblob {
			if err := backend.Deblob(ctx, &resolved.Image.Distro); err != nil {
				provisionErr = fmt.Errorf("deblob rootfs: %w", err)
				return
			}
		}
		// Fix fstab for Firecracker (superfloppy /dev/vda layout)
		if err := backend.FixFstab(ctx); err != nil {
			provisionErr = fmt.Errorf("fix fstab: %w", err)
			return
		}
		// --- Vsock agent injection ---
		// Queue guest agent binary, token, and init system integration into the rootfs.
		if resolved.VsockPort > 0 {
			builder.vsockPort = resolved.VsockPort
			builder.vsockToken = crypto.UUIDV4()
			builder.vsockUDSPath = vm.RuntimePath(os.Geteuid(), builder.vmID, infra.VMVsockSocketFilename)
			if agentBin := vsock.AgentBinary(); len(agentBin) > 0 {
				if err := backend.InjectAgent(ctx, agentBin, builder.vsockPort, builder.vsockToken); err != nil {
					provisionErr = fmt.Errorf("inject agent: %w", err)
					return
				}
			} else {
				slog.Warn("agent binary not available, skipping agent injection",
					"vm", resolved.Name)
			}
		}
		// Execute all queued provisioning operations
		if err := backend.Run(ctx); err != nil {
			provisionErr = fmt.Errorf("provision VM rootfs: %w", err)
			return
		}
	})
	if provisionErr != nil {
		return provisionErr
	}
	if cloudInitMarker != "" {
		builder.markCreated(cloudInitMarker)
	}
	if ciResultOut != nil {
		builder.cloudInitResult = ciResultOut
	}
	// --- Vsock CID allocation ---
	// Allocate a random guest CID if vsock is enabled. The CID is used in the
	// Firecracker JSON config (vsock section) and persisted to the DB after spawn.
	if builder.vsockPort > 0 {
		cid, err := op.Services.Vsock.AllocateCID()
		if err != nil {
			return err
		}
		builder.vsockCID = cid
	}
	// Progress: firecracker
	emitProgress(builder.onProgress, "firecracker", "running", "Starting Firecracker microVM...")
	// Firecracker config write, console relay, spawn (timed)
	var fcErr error
	tl.StageFunc("firecracker_spawn", func() {
		fcConfig := builder.buildFirecrackerConfig()
		if fcConfig == nil {
			fcErr = fmt.Errorf("firecracker config is nil")
			return
		}
		spawner := vm.NewFirecrackerSpawner(fcConfig)
		builder.fcManager = fcConfig
		if err := spawner.WriteToFile(); err != nil {
			fcErr = fmt.Errorf("write firecracker config: %w", err)
			return
		}
		// A launch failure may still own root cleanup authority.
		builder.markCreated("launch_attempted")
		// Spawn Firecracker
		if err := spawner.Spawn(ctx); err != nil {
			fcErr = fmt.Errorf("failed to spawn Firecracker: %w", err)
			return
		}
		// Store spawner for toVMModel()
		builder.spawner = spawner
	})
	if fcErr != nil {
		return fcErr
	}
	emitProgress(builder.onProgress, "complete", "complete", "VM created successfully")
	return nil
}

// vmBuilderCleanup preserves local artifacts whenever root cannot prove cleanup complete.
func (op *Operation) vmBuilderCleanup(ctx context.Context, builder *VMCreateBuilder) error {
	if builder.vmDir == "" || builder.resolved == nil {
		return nil
	}
	if builder.wasCreated("launch_attempted") {
		if _, err := jailer.NewVMLifecycleClient().Remove(ctx, jailer.VMCleanupRequest{VMID: builder.vmID}); err != nil {
			return fmt.Errorf("remove partial VM authority: %w", err)
		}
	}
	if err := op.Repos.Lease.ReleaseByVM(ctx, builder.vmID); err != nil {
		return fmt.Errorf("release partial VM lease: %w", err)
	}
	if builder.wasCreated("vm_dir") {
		if err := os.RemoveAll(builder.vmDir); err != nil {
			return fmt.Errorf("remove partial VM directory: %w", err)
		}
	}
	if err := op.Repos.VM.Delete(ctx, builder.vmID); err != nil {
		return fmt.Errorf("delete partial VM record: %w", err)
	}
	return nil
}

// --- Remove ---
// Remove removes one or more VMs.
func (op *Operation) VMRemove(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	vms, err := input.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(errs.CodeVMNotFound),
					Message: fmt.Sprintf("No VMs found matching the given identifiers: %v", err)},
			},
		}
	}
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
		// Enrichment is best-effort: enriched data improves inspect output but is not
		// required for the operation to succeed. A failed enrichment should not fail
		// the operation itself.
	}
	if len(vms) == 0 {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(errs.CodeVMNotFound),
					Message: "No VMs found matching the given identifiers"},
			},
		}
	}
	results := make([]errs.OperationResult, 0)
	// Report identifiers that couldn't be resolved
	unresolvedCount := len(input.Identifiers) - len(vms)
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
	for _, vmLocal := range vms {
		vmDir := infra.GetVMDirByID(vmLocal.ID)
		if err := vm.NewController(vmLocal, repo).Stop(ctx, input.Force); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.remove_failed",
				Item: vmLocal, Message: fmt.Sprintf("stop before remove '%s': %v", vmLocal.Name, err), Exception: err,
			})
			continue
		}
		// Root removes the exact process set and persistent endpoint authority before
		// any caller-owned record or artifact can disappear.
		if _, err := jailer.NewVMLifecycleClient().Remove(
			ctx, jailer.VMCleanupRequest{VMID: vmLocal.ID},
		); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: string(errs.CodeVMCgroupCleanupFailed),
				Item: vmLocal, Message: fmt.Sprintf("remove '%s': %v", vmLocal.Name, err), Exception: err,
			})
			continue
		}
		if err := op.Repos.Lease.ReleaseByVM(ctx, vmLocal.ID); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: string(errs.CodeDatabaseError), Item: vmLocal,
				Message: fmt.Sprintf("release lease for '%s': %v", vmLocal.Name, err), Exception: err,
			})
			continue
		}
		if err := op.Repos.TrafficPolicy.InvalidateByVM(ctx, vmLocal.ID); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: string(errs.CodeDatabaseError), Item: vmLocal,
				Message: fmt.Sprintf("invalidate traffic policies for '%s': %v", vmLocal.Name, err), Exception: err,
			})
			continue
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
		if err := os.RemoveAll(vmDir); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.remove_failed", Item: vmLocal,
				Message: fmt.Sprintf("remove files for '%s': %v", vmLocal.Name, err), Exception: err,
			})
			continue
		}
		// Delete from DB
		if err := repo.Delete(ctx, vmLocal.ID); err != nil {
			slog.Warn("failed to delete VM from DB", "vm", vmLocal.Name, "error", err)
			results = append(results, errs.OperationResult{
				Status: "error", Code: string(errs.CodeDatabaseError), Item: vmLocal,
				Message: fmt.Sprintf("failed to delete VM '%s': %v", vmLocal.Name, err), Exception: err,
			})
			continue
		}
		op.AuditLog.LogOperation("vm.remove", map[string]any{"name": vmLocal.Name}, "")
		results = append(results, errs.OperationResult{
			Status: "success", Code: "vm.removed",
			Item: vmLocal, Message: fmt.Sprintf("VM '%s' removed", vmLocal.Name),
		})
	}
	return &errs.BatchResult{Items: results}
}

// --- Prune ---
// Prune prunes VMs.
func (op *Operation) VMPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	allVMs, err := op.Repos.VM.ListAll(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, fmt.Sprintf("Failed to list VMs: %v", err), err)
	}
	if err := op.reconcileVMRuntimeStatus(ctx, allVMs); err != nil {
		return nil, err
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
				slog.Warn("Failed to remove VM", "name", vm.Name, "error", infra.JoinStringsPtrs(result))
				continue
			}
		}
		removed = append(removed, vm.Name)
	}
	return removed, nil
}

// --- List / ToJSON ---
// List returns all VMs, optionally filtered by status.
func (op *Operation) VMList(ctx context.Context, statuses ...string) ([]*model.VMItem, error) {
	vms, err := op.Repos.VM.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if err := op.reconcileVMRuntimeStatus(ctx, vms); err != nil {
		return nil, err
	}
	if len(statuses) > 0 {
		filtered := make([]*model.VMItem, 0, len(vms))
		for _, item := range vms {
			for _, status := range statuses {
				if string(item.Status) == status {
					filtered = append(filtered, item)
					break
				}
			}
		}
		vms = filtered
	}
	if len(vms) > 0 {
		// Relations enrich display only; root observations own the runtime status.
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
	}
	return vms, nil
}

// VMGet returns a single VM by identifier with enriched relations.
func (op *Operation) VMGet(ctx context.Context, input inputs.VMInput) (*model.VMItem, error) {
	if len(input.Identifiers) != 1 {
		return nil, fmt.Errorf("expected exactly one VM identifier")
	}
	// Use the full resolution pipeline (name, IP, MAC, ID prefix)
	vmResolver := vm.NewResolver(op.Repos.VM)
	vm, err := vmResolver.Resolve(ctx, input.Identifiers[0])
	if err != nil {
		return nil, err
	}
	if err := op.reconcileVMRuntimeStatus(ctx, []*model.VMItem{vm}); err != nil {
		return nil, err
	}
	// Enrich VM with relations
	op.Enr.EnrichVM(ctx, []*model.VMItem{vm}, "kernel", "image", "binary", "network", "volumes")
	return vm, nil
}

// Inspect returns detailed VM info with enriched data.
func (op *Operation) VMInspect(ctx context.Context, input inputs.VMInput) (*results.VMInspect, error) {
	vmItem, err := op.VMGet(ctx, input)
	if err != nil {
		return nil, err
	}
	// Enrich all relations at once instead of manual repo calls.
	if err := op.Enr.EnrichVM(ctx, []*model.VMItem{vmItem},
		"kernel", "image", "binary", "network", "volumes", "vsock",
	); err != nil {
		return nil, err
	}
	relayRunning := vmItem.RelayPID != nil && system.IsProcessRunning(*vmItem.RelayPID)
	vmDir := infra.GetVMDirByID(vmItem.ID)
	// Volumes (enriched vm.Volumes or fallback to manual lookup).
	volumes := make([]results.VMVolume, 0)
	srcVols := vmItem.Volumes
	if len(srcVols) == 0 && len(vmItem.VolumeIDs) > 0 {
		vols, err := op.Repos.Volume.FindByIDs(ctx, vmItem.VolumeIDs)
		if err == nil {
			srcVols = vols
		}
	}
	for _, v := range srcVols {
		volumes = append(volumes, results.VMVolume{
			ID: v.ID, Name: v.Name, Size: v.SizeBytes,
			Format: string(v.Format), Status: string(v.Status),
		})
	}
	configPath := &vmItem.ConfigPath
	if vmItem.ConfigPath == "" {
		configPath = nil
	}
	if vmItem.CgroupLimits.PolicyVersion == 0 {
		vmItem.CgroupLimits = op.resolveVMCgroupLimits(ctx, vmItem.VCPUCount, vmItem.MemSizeMiB)
	}
	cgroupState := model.VMCgroupState{
		Path: vm.CgroupPath(vmItem.ID), Requested: vmItem.CgroupLimits, Status: model.VMCgroupEnforcementInactive,
	}
	if vmItem.PID > 0 && system.IsProcessAlive(vmItem.PID, vmItem.ProcessStartTime) {
		observed, inspectErr := vm.InspectCgroup(ctx, vmItem.ID, vmItem.PID, vmItem.CgroupLimits)
		if inspectErr != nil {
			cgroupState.Status = model.VMCgroupEnforcementUnavailable
			cgroupState.Mismatches = []string{inspectErr.Error()}
		} else {
			cgroupState = *observed
		}
	}
	return &results.VMInspect{
		VM: results.VMItemInfo{
			Name: vmItem.Name, ID: vmItem.ID, Status: string(vmItem.Status),
			PID: vmItem.PID, ExitCode: vmItem.ExitCode,
			SSHKeys: vmItem.SSHKeys, SSHUser: vmItem.SSHUser,
			CloudInitMode:  vmItem.CloudInitMode,
			NocloudNetPort: vmItem.NocloudNetPort, NocloudNetPID: vmItem.NocloudNetPID,
			PCIEnabled: vmItem.PCIEnabled, NestedVirt: vmItem.NestedVirt,
			EnableConsole: vmItem.EnableConsole,
			EnableLogging: vmItem.EnableLogging, EnableMetrics: vmItem.EnableMetrics,
			CreatedAt: vmItem.CreatedAt, UpdatedAt: vmItem.UpdatedAt,
		},
		Resources: results.VMResourcesInfo{
			VCPU: vmItem.VCPUCount, Mem: vmItem.MemSizeMiB, Disk: vmItem.DiskSizeMiB, Cgroup: cgroupState,
		},
		Networking: results.VMNetworkingInfo{
			IPv4: vmItem.IPv4, MAC: vmItem.MAC,
			Network: vmItem.Network, TapDevice: vmItem.TapDevice,
		},
		Assets: results.VMAssetsInfo{
			Image:  vmItem.Image,
			Kernel: vmItem.Kernel,
			Binary: vmItem.Binary,
		},
		Filesystem: results.VMFilesystemInfo{
			VMDir: vmDir, RootfsPath: vmItem.RootfsPath,
			ConfigPath:       configPath,
			LogPath:          vmItem.LogPath,
			SerialOutputPath: vmItem.SerialOutputPath,
		},
		Console: results.VMConsoleInfo{
			RelayRunning: relayRunning, RelayPID: vmItem.RelayPID,
			RelaySocketPath: vmItem.RelaySocketPath,
		},
		Volumes: volumes,
		Vsock:   vsockToInspectInfo(vmItem.Vsock),
	}, nil
}

// vsockToInspectInfo converts a model.VsockConfigItem to a results.VMVsockInfo,
// intentionally dropping the Token field to avoid leaking the auth secret.
func vsockToInspectInfo(v *model.VsockConfigItem) *results.VMVsockInfo {
	if v == nil {
		return nil
	}
	return &results.VMVsockInfo{
		ID:               v.ID,
		GuestCID:         v.GuestCID,
		UDSPath:          v.UDSPath,
		Port:             v.Port,
		AgentVersion:     v.AgentVersion,
		Upgrading:        v.Upgrading,
		UpgradeStartedAt: v.UpgradeStartedAt,
	}
}

// --- Start / Stop / Reboot / Pause / Resume ---

type vmRelaunchState struct {
	vm      *model.VMItem
	vsock   *model.VsockConfigItem
	volumes []*model.VolumeItem
}

func (op *Operation) resolveVMVolumes(ctx context.Context, vmItem *model.VMItem) ([]*model.VolumeItem, error) {
	if len(vmItem.VolumeIDs) == 0 {
		return nil, nil
	}
	input := inputs.VolumeInput{Identifiers: vmItem.VolumeIDs}
	volumes, err := input.ResolveRaw(ctx, op.Repos.Volume)
	if err != nil {
		return nil, err
	}
	// Persisted IDs are exact references, unlike user-facing names and ID prefixes.
	resolvedIDs := make(map[string]bool, len(volumes))
	for _, vol := range volumes {
		resolvedIDs[vol.ID] = true
	}
	for _, id := range vmItem.VolumeIDs {
		if !resolvedIDs[id] {
			return nil, errs.NotFound(errs.CodeVolumeNotFound,
				fmt.Sprintf("persisted volume ID %q was not resolved exactly", id))
		}
	}
	return volumes, nil
}

func (op *Operation) loadVMRelaunchState(ctx context.Context, vmItem *model.VMItem) (*vmRelaunchState, error) {
	volumes, err := op.resolveVMVolumes(ctx, vmItem)
	if err != nil {
		return nil, err
	}
	row, err := op.Repos.Vsock.GetByVMID(ctx, vmItem.ID)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, "failed to load VM vsock relaunch state", err)
	}
	state := &vmRelaunchState{vm: vmItem, volumes: volumes}
	if row == nil {
		return state, nil
	}
	if row.VmID != vmItem.ID {
		return nil, errs.New(errs.CodeDatabaseError, "persisted vsock configuration has a mismatched VM identity")
	}
	if !model.IsValidVsockGuestCID(row.GuestCID) {
		return nil, errs.New(errs.CodeDatabaseError, "persisted vsock configuration has an invalid guest CID")
	}
	cloned := *row
	if row.UpgradeStartedAt != nil {
		started := *row.UpgradeStartedAt
		cloned.UpgradeStartedAt = &started
	}
	cloned.UDSPath = vm.RuntimePath(os.Geteuid(), vmItem.ID, infra.VMVsockSocketFilename)
	state.vsock = &cloned
	return state, nil
}

func (op *Operation) relaunchVM(
	ctx context.Context,
	state *vmRelaunchState,
	snapshotMode bool,
	snapshotDir string,
) error {
	state.vm.Vsock = state.vsock
	state.vm.Volumes = state.volumes
	if op.relaunchSpawn != nil {
		return op.relaunchSpawn(ctx, state, snapshotMode, snapshotDir)
	}
	return op.vmRespawnFirecracker(ctx, state, snapshotMode, snapshotDir)
}

// Start starts one or more VMs.
func (op *Operation) VMStart(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)
	vms, resolveErr := input.Resolve(ctx, repo)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.start_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}
	if err := op.reconcileVMRuntimeStatus(ctx, vms); err != nil {
		return &errs.BatchResult{Items: []errs.OperationResult{{
			Status: "error", Code: "vm.start_failed", Message: err.Error(), Exception: err,
		}}}
	}
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases")
		// Enrichment is best-effort: enriched data improves inspect output but is not
		// required for the operation to succeed. A failed enrichment should not fail
		// the operation itself.
	}
	for _, vmLocal := range vms {
		// If VM is stopped, respawn Firecracker process
		if vmLocal.Status == model.VMStatusStopped || vmLocal.Status == model.VMStatusCrashed {
			state, err := op.loadVMRelaunchState(ctx, vmLocal)
			if err == nil {
				err = op.relaunchVM(ctx, state, false, "")
			}
			if err != nil {
				results = append(results, errs.OperationResult{
					Status: "error", Code: "vm.start_failed",
					Message:   fmt.Sprintf("start '%s': %v", vmLocal.Name, err),
					Exception: err,
				})
				continue
			}
		} else {
			if _, err := op.resolveVMVolumes(ctx, vmLocal); err != nil {
				results = append(results, errs.OperationResult{
					Status: "error", Code: "vm.start_failed",
					Message:   fmt.Sprintf("start '%s': %v", vmLocal.Name, err),
					Exception: err,
				})
				continue
			}
			controller := vm.NewController(vmLocal, repo)
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
func (op *Operation) VMStop(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)
	// Batch resolve all VMs first.
	vms, resolveErr := input.Resolve(ctx, op.Repos.VM)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.stop_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
		// Enrichment is best-effort: enriched data improves inspect output but is not
		// required for the operation to succeed. A failed enrichment should not fail
		// the operation itself.
	}
	for _, vmLocal := range vms {
		controller := vm.NewController(vmLocal, repo)
		if err := controller.Stop(ctx, input.Force); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.stop_failed",
				Item: vmLocal, Message: fmt.Sprintf("stop '%s': %v", vmLocal.Name, err), Exception: err,
			})
			continue
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
func (op *Operation) vmRespawnFirecracker(
	ctx context.Context,
	state *vmRelaunchState,
	snapshotMode bool,
	snapshotDir string,
) error {
	if snapshotMode || snapshotDir != "" {
		return errs.New(errs.CodeSnapshotRestoreFailed,
			"snapshot restore requires root-owned capture and backing authority")
	}
	v := state.vm
	if v.CgroupLimits.PolicyVersion == 0 {
		v.CgroupLimits = op.resolveVMCgroupLimits(ctx, v.VCPUCount, v.MemSizeMiB)
		if err := op.Repos.VM.Upsert(ctx, v); err != nil {
			return fmt.Errorf("persist migrated VM cgroup limits: %w", err)
		}
	}
	vmDir := infra.GetVMDirByID(v.ID)
	// Network info is required — the VM record must have it pre-loaded.
	if v.Network == nil {
		return fmt.Errorf("network info is required for VM respawn")
	}
	if v.Binary == nil || v.Binary.Path == "" {
		return fmt.Errorf("binary info is required for VM respawn")
	}
	pair, err := op.Services.Binary.ResolvePair(ctx, v.Binary)
	if err != nil {
		return err
	}
	if v.JailerBinaryID != "" && pair.Jailer.ID != v.JailerBinaryID {
		return errs.New(errs.CodeBinaryPairInvalid,
			fmt.Sprintf("persisted Jailer does not match Firecracker version %s", v.Binary.Version))
	}
	if v.KernelID == "" || v.Kernel == nil || v.Kernel.Path == "" {
		return fmt.Errorf("kernel info is required for VM respawn")
	}
	managedRootfsPath := filepath.Join(vmDir, infra.VMRootfsFilename)
	if _, err := os.Stat(managedRootfsPath); err != nil {
		return fmt.Errorf("managed VM rootfs is unavailable: %w", err)
	}
	if v.ImageID != "" && v.Image == nil {
		return fmt.Errorf("image info is required for VM respawn")
	}
	if err := vm.CleanupJail(ctx, v.ID); err != nil {
		return fmt.Errorf("clean VM before respawn: %w", err)
	}
	logLevel, _ := op.Services.Config.GetString(ctx, "defaults.firecracker", "log_level")
	fcConfig := buildRespawnFirecrackerConfig(state, pair, vmDir, snapshotMode, snapshotDir, logLevel)
	fcConfig.CacheRoot = op.CacheDir
	tapName, err := networkservice.VMTAPName(uint32(os.Geteuid()), v.NetworkID, v.ID)
	if err != nil {
		return err
	}
	fcConfig.TapName = tapName
	// --- Write config and spawn ---
	spawner := vm.NewFirecrackerSpawner(fcConfig)
	if err := spawner.WriteToFile(); err != nil {
		return fmt.Errorf("write firecracker config: %w", err)
	}
	if err := spawner.Spawn(ctx); err != nil {
		slog.Warn("Failed to respawn Firecracker", "vm", v.Name, "error", err)
		return err
	}
	v.PID = *spawner.PID
	v.ProcessStartTime = spawner.ProcessStartTime
	v.Status = model.VMStatusRunning
	v.APISocketPath = spawner.APISocketPath
	v.TapDevice = fcConfig.TapName
	if v.EnableConsole {
		socketPath := vm.RuntimePath(os.Geteuid(), v.ID, infra.VMConsoleSocketFilename)
		v.RelaySocketPath = &socketPath
	}
	if err := op.Repos.VM.Upsert(ctx, v); err != nil {
		_, cleanupErr := jailer.NewVMLifecycleClient().Cleanup(
			context.WithoutCancel(ctx), jailer.VMCleanupRequest{VMID: v.ID},
		)
		return errors.Join(fmt.Errorf("persist relaunched VM: %w", err), cleanupErr)
	}
	return nil
}

// buildRespawnFirecrackerConfig reconstructs launch paths from the managed VM
// directory. Persisted path columns are descriptive metadata, never authority.
func buildRespawnFirecrackerConfig(
	state *vmRelaunchState,
	pair *model.BinaryPair,
	vmDir string,
	snapshotMode bool,
	snapshotDir string,
	logLevel string,
) *model.FirecrackerConfig {
	v := state.vm
	config := &model.FirecrackerConfig{
		CacheRoot:           filepath.Dir(filepath.Dir(vmDir)),
		KernelID:            v.KernelID,
		NetworkID:           v.NetworkID,
		ReleaseArchitecture: pair.Firecracker.Architecture,
		VMDir:               vmDir,
		RootfsPath:          filepath.Join(vmDir, infra.VMRootfsFilename),
		BinaryPath:          v.Binary.Path,
		JailerPath:          pair.Jailer.Path,
		BinaryVersion:       pair.Firecracker.Version,
		KernelPath:          v.Kernel.Path,
		VCPUCount:           v.VCPUCount,
		MemSizeMiB:          v.MemSizeMiB,
		CgroupLimits:        v.CgroupLimits,
		GuestIP:             v.IPv4,
		GuestMAC:            v.MAC,
		TapName:             v.TapDevice,
		NetworkGateway:      v.Network.IPv4Gateway,
		PCIEnabled:          v.PCIEnabled,
		NestedVirt:          v.NestedVirt,
		EnableConsole:       v.EnableConsole,
		EnableLogging:       v.EnableLogging,
		EnableMetrics:       v.EnableMetrics,
		BootArgs:            v.BootArgs,
		LSMFlags:            v.LSMFlags,
		SnapshotMode:        snapshotMode,
		SnapshotDir:         snapshotDir,
		ConfigPath:          filepath.Join(vmDir, infra.VMFirecrackerConfigFilename),
		APISocketPath:       vm.RuntimePath(os.Geteuid(), v.ID, infra.VMFirecrackerAPISocketFilename),
		LogPath:             filepath.Join(vmDir, infra.VMFirecrackerLogFilename),
		SerialOutputPath:    filepath.Join(vmDir, infra.VMFirecrackerConsoleLogFilename),
		MetricsPath:         filepath.Join(vmDir, infra.VMFirecrackerMetricsFilename),
		LogLevel:            logLevel,
		PIDPath:             vm.RuntimePath(os.Geteuid(), v.ID, infra.VMFirecrackerPIDFilename),
	}
	if v.Image != nil {
		config.ImageFSUUID = v.Image.FSUUID
		config.ImageFSType = v.Image.FSType
	}
	cloudInitMode := model.CloudInitMode(v.CloudInitMode)
	config.CloudInitMode = &cloudInitMode
	if cloudInitMode == model.CloudInitModeISO {
		isoPath := filepath.Join(vmDir, infra.VMCloudInitISOFilename)
		config.CloudInitISOPath = &isoPath
	}
	config.CPUConfig = v.CPUConfig
	if state.vsock != nil {
		cid := state.vsock.GuestCID
		config.VsockGuestCID = &cid
	}
	if len(state.volumes) > 0 {
		config.ExtraDrives = volume.VolumesToDrives(state.volumes)
	}
	return config
}

func (op *Operation) resolveVMCgroupLimits(ctx context.Context, vcpuCount, memoryMiB int) model.VMCgroupLimits {
	headroomMiB, _ := op.Services.Config.GetInt(ctx, "defaults.vm", "cgroup_vmm_headroom_mib")
	cpuWeight, _ := op.Services.Config.GetInt(ctx, "defaults.vm", "cgroup_cpu_weight")
	pidsMax, _ := op.Services.Config.GetInt(ctx, "defaults.vm", "cgroup_pids_max")
	swapMaxBytes, _ := op.Services.Config.GetInt(ctx, "defaults.vm", "cgroup_swap_max_bytes")
	return model.NewVMCgroupLimits(vcpuCount, memoryMiB, model.VMCgroupPolicy{
		VMMHeadroomMiB: int64(headroomMiB),
		CPUWeight:      int64(cpuWeight),
		PIDsMax:        int64(pidsMax),
		SwapMaxBytes:   int64(swapMaxBytes),
	})
}

// --- Reboot / Pause / Resume ---
// Reboot reboots one or more VMs.
func (op *Operation) VMReboot(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)
	// Batch resolve all VMs first.
	vms, resolveErr := input.Resolve(ctx, op.Repos.VM)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.reboot_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases")
		// Enrichment is best-effort: enriched data improves inspect output but is not
		// required for the operation to succeed. A failed enrichment should not fail
		// the operation itself.
	}
	for _, vmLocal := range vms {
		state, err := op.loadVMRelaunchState(ctx, vmLocal)
		if err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.reboot_failed",
				Message:   fmt.Sprintf("reboot '%s': %v", vmLocal.Name, err),
				Exception: err,
			})
			continue
		}
		// Stop the VM first (kills the firecracker process)
		controller := vm.NewController(vmLocal, repo)
		if err := controller.Stop(ctx, input.Force); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.reboot_failed",
				Message: fmt.Sprintf("stop before reboot '%s': %v", vmLocal.Name, err), Exception: err,
			})
			continue
		}
		// After stop, respawn a fresh firecracker process
		if err := op.relaunchVM(ctx, state, false, ""); err != nil {
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
func (op *Operation) VMPause(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)
	// Batch resolve all VMs first.
	vms, resolveErr := input.Resolve(ctx, op.Repos.VM)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.pause_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
		// Enrichment is best-effort: enriched data improves inspect output but is not
		// required for the operation to succeed. A failed enrichment should not fail
		// the operation itself.
	}
	for _, vmLocal := range vms {
		controller := vm.NewController(vmLocal, repo)
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
func (op *Operation) VMResume(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	repo := op.Repos.VM
	results := make([]errs.OperationResult, 0)
	// Batch resolve all VMs first.
	vms, resolveErr := input.Resolve(ctx, op.Repos.VM)
	if resolveErr != nil {
		for _, ident := range input.Identifiers {
			results = append(results, errs.OperationResult{
				Status: "error", Code: "vm.resume_failed",
				Message: fmt.Sprintf("VM not found: %s (%v)", ident, resolveErr),
			})
		}
		return &errs.BatchResult{Items: results}
	}
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
		// Enrichment is best-effort: enriched data improves inspect output but is not
		// required for the operation to succeed. A failed enrichment should not fail
		// the operation itself.
	}
	for _, vmLocal := range vms {
		controller := vm.NewController(vmLocal, repo)
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

// --- Builder ---
type VMCreateBuilder struct {
	cacheRoot        string
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
	cloudInitResult  *cloudInitResult
	resourcesCreated map[string]bool
	// Vsock state (set during vmBuilderExecute, used in buildFirecrackerConfig and post-spawn)
	vsockCID     int
	vsockPort    int
	vsockUDSPath string
	vsockToken   string
}
type cloudInitResult struct {
	mode        model.CloudInitMode
	isoPath     *string
	nocloudURL  *string
	nocloudPort *int
	nocloudPID  *int
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
	vmRootfsPath := filepath.Join(c.vmDir, infra.VMRootfsFilename)
	if _, err := imageSvc.EnsureCached([]*model.ImageItem{resolved.Image}); err != nil {
		return fmt.Errorf("ensure cached image: %w", err)
	}
	if err := imageSvc.MaterializeTo(ctx, resolved.Image.ID, fsType, vmRootfsPath); err != nil {
		return fmt.Errorf("materialize image: %w", err)
	}
	if err := os.Chmod(vmRootfsPath, 0600); err != nil {
		return fmt.Errorf("set private VM rootfs permissions: %w", err)
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
func (c *VMCreateBuilder) buildFirecrackerConfig() *model.FirecrackerConfig {
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
		CacheRoot:           c.cacheRoot,
		KernelID:            c.resolved.Kernel.ID,
		NetworkID:           c.resolved.Network.ID,
		ReleaseArchitecture: c.resolved.Binary.Architecture,
		VMDir:               c.vmDir,
		RootfsPath:          c.rootfsPath,
		BinaryPath:          c.resolved.Binary.Path,
		JailerPath:          c.resolved.Jailer.Path,
		BinaryVersion:       c.resolved.Binary.Version,
		KernelPath:          c.resolved.Kernel.Path,
		VCPUCount:           c.resolved.VCPUCount,
		MemSizeMiB:          c.resolved.MemSizeMib,
		CgroupLimits:        c.resolved.CgroupLimits,
		GuestIP:             c.guestIP,
		GuestMAC:            c.guestMAC,
		TapName:             c.tapName,
		NetworkGateway:      c.resolved.Network.IPv4Gateway,
		NetworkNetmask:      c.resolved.NetworkNetmask,
		ImageFSUUID:         c.resolved.Image.FSUUID,
		ImageFSType:         c.resolved.Image.FSType,
		BootArgs:            c.resolved.BootArgs,
		LSMFlags:            c.resolved.LSMFlags,
		PCIEnabled:          c.resolved.PCIEnabled,
		NestedVirt:          c.resolved.NestedVirt,
		CPUVendor:           cpuVendor,
		CPUArchitecture:     cpuArchitecture,
		CloudInitMode:       &ciMode,
		EnableConsole:       c.resolved.EnableConsole,
		EnableLogging:       c.resolved.EnableLogging,
		EnableMetrics:       c.resolved.EnableMetrics,
		LogLevel:            c.resolved.LogLevel,
		ExtraDrives:         c.resolved.ExtraDrives,
		Writeback:           c.resolved.Writeback,
		// Fixed managed paths; field names match DB columns used during respawn.
		ConfigPath:       filepath.Join(c.vmDir, infra.VMFirecrackerConfigFilename),
		APISocketPath:    vm.RuntimePath(os.Geteuid(), c.vmID, infra.VMFirecrackerAPISocketFilename),
		LogPath:          filepath.Join(c.vmDir, infra.VMFirecrackerLogFilename),
		SerialOutputPath: filepath.Join(c.vmDir, infra.VMFirecrackerConsoleLogFilename),
		MetricsPath:      filepath.Join(c.vmDir, infra.VMFirecrackerMetricsFilename),
		PIDPath:          vm.RuntimePath(os.Geteuid(), c.vmID, infra.VMFirecrackerPIDFilename),
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
	// Vsock device config
	if model.IsValidVsockGuestCID(c.vsockCID) {
		cid := c.vsockCID
		fcConfig.VsockGuestCID = &cid
	}
	return fcConfig
}
func (c *VMCreateBuilder) toVMModel() *model.VMItem {
	if c.resolved == nil || c.spawner == nil || c.spawner.PID == nil {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	logPath := filepath.Join(c.vmDir, infra.VMFirecrackerLogFilename)
	serialPath := filepath.Join(c.vmDir, infra.VMFirecrackerConsoleLogFilename)
	item := &model.VMItem{
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
		JailerBinaryID:   c.resolved.Jailer.ID,
		VCPUCount:        c.resolved.VCPUCount,
		MemSizeMiB:       c.resolved.MemSizeMib,
		CgroupLimits:     c.resolved.CgroupLimits,
		DiskSizeMiB:      c.resolved.DiskSizeMib,
		APISocketPath:    vm.RuntimePath(os.Geteuid(), c.vmID, infra.VMFirecrackerAPISocketFilename),
		ConfigPath:       filepath.Join(c.vmDir, infra.VMFirecrackerConfigFilename),
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
			item.SSHKeys = append(item.SSHKeys, k.Name)
		}
	}
	if c.resolved.CPUConfig != nil {
		item.CPUConfig = c.resolved.CPUConfig
	}
	if c.resolved.BootArgs != "" {
		item.BootArgs = c.resolved.BootArgs
	}
	if c.resolved.LSMFlags != "" {
		item.LSMFlags = c.resolved.LSMFlags
	}
	// Nocloud port/pid from cloud_init_result (prefer runtime values over resolved)
	if c.cloudInitResult != nil && c.cloudInitResult.nocloudPort != nil {
		item.NocloudNetPort = c.cloudInitResult.nocloudPort
		if c.cloudInitResult.nocloudPID != nil {
			item.NocloudNetPID = c.cloudInitResult.nocloudPID
		}
	} else if c.resolved.NocloudNetPort != nil {
		item.NocloudNetPort = c.resolved.NocloudNetPort
	}
	if c.resolved.EnableConsole {
		socketPath := vm.RuntimePath(os.Geteuid(), c.vmID, infra.VMConsoleSocketFilename)
		item.RelaySocketPath = &socketPath
	}
	return item
}
