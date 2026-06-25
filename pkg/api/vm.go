// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
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
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/infra/timinglog"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	libnet "mvmctl/internal/lib/network"
	"mvmctl/internal/lib/provisioner"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
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
	VMList(ctx context.Context, statuses ...string) []*model.VMItem
	VMGet(ctx context.Context, input inputs.VMInput) (*model.VMItem, error)
	VMInspect(ctx context.Context, input inputs.VMInput) (*results.VMInspect, error)
	VMStart(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMStop(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMReboot(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMPause(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMResume(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMAttachVolume(ctx context.Context, input inputs.VMInput, volumeName string) error
	VMDetachVolume(ctx context.Context, input inputs.VMInput, volumeName string) error
	VMExec(ctx context.Context, input inputs.VMExecInput) (*results.VMExecResult, error)
}

// --- Create ---
// Create creates one or more VMs.
func (op *Operation) VMCreate(
	ctx context.Context,
	input inputs.VMCreateInput,
	onProgress event.OnProgressCallback,
) ([]*model.VMItem, error) {
	if err := system.CheckPrivileges("/usr/sbin/ip", "create VMs"); err != nil {
		return nil, errs.WrapMsg(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("Privilege check failed: %v", err),
			err,
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}
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
	sharedResolved.Provisioner = model.ProvisionerType(op.ProvisionerType)
	// Network bridge setup (shared across all VMs in the batch, done once).
	bridgeAddr, calcErr := network.ComputeBridgeAddress(
		sharedResolved.Network.IPv4Gateway, sharedResolved.Network.Subnet,
	)
	if calcErr != nil {
		return nil, fmt.Errorf("compute bridge address: %w", calcErr)
	}
	// Pre-compute TAP names so we can add firewall rules in the shared batch.
	tapNames := make([]string, len(names))
	for i, name := range names {
		tapNames[i] = libnet.VMGenerateTAPName(sharedResolved.Network.Name, name)
	}
	// Bridge + NAT + TAP firewall rules in a single batch for atomic application.
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
		// TAP FORWARD rules for all VMs in the batch.
		for _, tapName := range tapNames {
			if err := op.Services.Network.AddTapFirewallRules(ctx, tapName,
				sharedResolved.Network.Bridge, sharedResolved.Network.ID,
				sharedResolved.Network.Subnet); err != nil {
				slog.Warn("failed to add TAP firewall rules during VM creation",
					"tap", tapName, "error", err)
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
	var vmInstance *model.VMItem
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
				slog.Error("failed to persist vsock config",
					"vm", builder.name, "error", err)
			}
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
	builder.tapName = libnet.VMGenerateTAPName(resolved.Network.Name, resolved.Name)
	// Create VM directory
	if err := os.MkdirAll(builder.vmDir, 0755); err != nil {
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
		// TAP device creation (firewall rules were added in the shared batch before goroutines).
		if err := op.Services.Network.EnsureTapDevice(ctx, builder.tapName, resolved.Network.Bridge); err != nil {
			networkErr = fmt.Errorf("ensure TAP: %w", err)
			return
		}
	})
	if networkErr != nil {
		return networkErr
	}
	builder.markCreated("network_tap")
	libnet.FlushARP(ctx, resolved.Network.Bridge)
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
				CloudInitISOName:      resolved.CloudInitISOName,
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
			builder.vsockUDSPath = filepath.Join(builder.vmDir, resolved.VsockFilename)
			if agentBin := vsock.AgentBinary(); len(agentBin) > 0 {
				if err := backend.InjectVsockAgent(ctx, agentBin, builder.vsockPort, builder.vsockToken); err != nil {
					provisionErr = fmt.Errorf("inject vsock agent: %w", err)
					return
				}
			} else {
				slog.Warn("vsock agent binary not available, skipping agent injection",
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
	var (
		fcErr            error
		firecrackerReady bool
		consoleRelayUp   bool
	)
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
		firecrackerReady = true
		// Console relay setup (before spawn)
		if resolved.EnableConsole {
			consoleCtrl := console.NewController(builder.vmID, builder.vmDir, builder.name,
				resolved.ConsolePIDFilename, resolved.ConsoleSocketFilename)
			ptyFD, ptyErr := consoleCtrl.CreatePTY()
			if ptyErr != nil {
				fcErr = fmt.Errorf("console PTY creation failed: %w", ptyErr)
				return
			}
			builder.relay = consoleCtrl
			fcConfig.RelayClientFD = &ptyFD
		}
		// Spawn Firecracker
		if err := spawner.Spawn(); err != nil {
			fcErr = fmt.Errorf("failed to spawn Firecracker: %w", err)
			return
		}
		// Store spawner for toVMModel()
		builder.spawner = spawner
		// Start console relay after spawn
		if resolved.EnableConsole && builder.relay != nil {
			builder.relay.CloseClientFD()
			_, _, startErr := builder.relay.Start(ctx)
			if startErr != nil {
				fcErr = fmt.Errorf("console relay start failed: %w", startErr)
				return
			}
			consoleRelayUp = true
		}
	})
	if fcErr != nil {
		return fcErr
	}
	if firecrackerReady {
		builder.markCreated("firecracker")
	}
	if consoleRelayUp {
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
			slog.Debug("failed to remove TAP during cleanup", "vm", builder.name, "error", tapErr)
		}
		if leaseErr := op.Repos.Lease.ReleaseByVM(ctx, builder.vmID); leaseErr != nil {
			slog.Debug("failed to release lease during cleanup", "vm", builder.name, "error", leaseErr)
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
			slog.Debug("failed to remove VM directory during cleanup", "vm", builder.name, "error", rmErr)
		}
	}
}

// --- Remove ---
// Remove removes one or more VMs.
func (op *Operation) VMRemove(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	if err := system.CheckPrivileges("/usr/sbin/ip", "Remove VM"); err != nil {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(errs.CodePrivilegeRequired),
					Message: fmt.Sprintf("Privilege check failed: %v", err), Exception: err},
			},
		}
	}
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
		// Stop the VM
		controller := vm.NewController(vmLocal, repo)
		controller.Stop(ctx, input.Force)
		// Defense-in-depth: force-kill
		if vmLocal.PID > 0 && system.IsProcessRunning(vmLocal.PID) {
			proc, err := os.FindProcess(vmLocal.PID)
			if err == nil {
				_ = proc.Kill()
			}
		}
		// Console relay, TAP, IP lease cleanup
		if vmLocal.RelayPID != nil {
			system.GracefulShutdown(system.ShutdownConfig{
				Pid:             *vmLocal.RelayPID,
				GracefulTimeout: 1 * time.Second,
				KillTimeout:     1 * time.Millisecond,
			})
		}
		if vmLocal.RelaySocketPath != nil {
			_ = os.Remove(*vmLocal.RelaySocketPath)
		}
		// Nocloudnet server cleanup
		if vmLocal.NocloudNetPID != nil {
			system.GracefulShutdown(system.ShutdownConfig{
				Pid:             *vmLocal.NocloudNetPID,
				GracefulTimeout: 1 * time.Second,
				KillTimeout:     1 * time.Millisecond,
			})
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
		if err := repo.Delete(ctx, vmLocal.ID); err != nil {
			slog.Warn("failed to delete VM from DB", "vm", vmLocal.Name, "error", err)
		}
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

// --- Prune ---
// Prune prunes VMs.
func (op *Operation) VMPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if err := system.CheckPrivileges("/usr/sbin/ip", "prune VMs"); err != nil {
		return nil, errs.WrapMsg(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("Privilege check failed: %v", err),
			err,
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}
	allVMs, err := op.Repos.VM.ListAll(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, fmt.Sprintf("Failed to list VMs: %v", err), err)
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
func (op *Operation) VMList(ctx context.Context, statuses ...string) []*model.VMItem {
	var vms []*model.VMItem
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
	// Enrich VM with relations
	op.Enr.EnrichVM(ctx, []*model.VMItem{vm}, "kernel", "image", "binary", "network", "network.leases", "volumes")
	return vm, nil
}

// Inspect returns detailed VM info with enriched data.
func (op *Operation) VMInspect(ctx context.Context, input inputs.VMInput) (*results.VMInspect, error) {
	vm, err := op.VMGet(ctx, input)
	if err != nil {
		return nil, err
	}
	// Enrich all relations at once instead of manual repo calls.
	if err := op.Enr.EnrichVM(ctx, []*model.VMItem{vm},
		"kernel", "image", "binary", "network", "network.leases", "volumes",
	); err != nil {
		return nil, err
	}
	relayRunning := vm.RelayPID != nil && system.IsProcessRunning(*vm.RelayPID)
	vmDir := infra.GetVMDirByID(vm.ID)
	// Volumes (enriched vm.Volumes or fallback to manual lookup).
	volumes := make([]results.VMVolume, 0)
	srcVols := vm.Volumes
	if len(srcVols) == 0 && len(vm.VolumeIDs) > 0 {
		vols, err := op.Repos.Volume.FindByIDs(ctx, vm.VolumeIDs)
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
	configPath := &vm.ConfigPath
	if vm.ConfigPath == "" {
		configPath = nil
	}
	return &results.VMInspect{
		VM: results.VMItemInfo{
			Name: vm.Name, ID: vm.ID, Status: string(vm.Status),
			PID: vm.PID, ExitCode: vm.ExitCode,
			SSHKeys: vm.SSHKeys, SSHUser: vm.SSHUser,
			CloudInitMode:  vm.CloudInitMode,
			NocloudNetPort: vm.NocloudNetPort, NocloudNetPID: vm.NocloudNetPID,
			PCIEnabled: vm.PCIEnabled, EnableConsole: vm.EnableConsole,
			EnableLogging: vm.EnableLogging, EnableMetrics: vm.EnableMetrics,
			CreatedAt: vm.CreatedAt, UpdatedAt: vm.UpdatedAt,
		},
		Resources: results.VMResourcesInfo{
			VCPU: vm.VCPUCount, Mem: vm.MemSizeMiB, Disk: vm.DiskSizeMiB,
		},
		Networking: results.VMNetworkingInfo{
			IPv4: vm.IPv4, MAC: vm.MAC,
			Network: vm.Network, TapDevice: vm.TapDevice,
		},
		Assets: results.VMAssetsInfo{
			Image:  vm.Image,
			Kernel: vm.Kernel,
			Binary: vm.Binary,
		},
		Filesystem: results.VMFilesystemInfo{
			VMDir: vmDir, RootfsPath: vm.RootfsPath,
			ConfigPath:       configPath,
			LogPath:          vm.LogPath,
			SerialOutputPath: vm.SerialOutputPath,
		},
		Console: results.VMConsoleInfo{
			RelayRunning: relayRunning, RelayPID: vm.RelayPID,
			RelaySocketPath: vm.RelaySocketPath,
		},
		Volumes: volumes,
	}, nil
}

// --- Start / Stop / Reboot / Pause / Resume ---
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
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
		// Enrichment is best-effort: enriched data improves inspect output but is not
		// required for the operation to succeed. A failed enrichment should not fail
		// the operation itself.
	}
	for _, vmLocal := range vms {
		// If VM is stopped, respawn Firecracker process
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
func (op *Operation) vmRespawnFirecracker(ctx context.Context, v *model.VMItem, snapshotMode bool) error {
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
	if v.ImageID != "" && v.Image == nil {
		return fmt.Errorf("image info is required for VM respawn")
	}
	// --- Force-kill any remaining Firecracker process ---
	if v.PID > 0 && system.IsProcessRunning(v.PID) {
		system.GracefulShutdown(system.ShutdownConfig{
			Pid:             v.PID,
			GracefulTimeout: 100 * time.Millisecond,
			KillTimeout:     50 * time.Millisecond,
		})
	}
	// --- Batch: bridge, NAT, TAP, then ARP flush ---
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
				if err := op.Services.Network.AddTapFirewallRules(ctx, v.TapDevice,
					v.Network.Bridge, v.Network.ID, v.Network.Subnet); err != nil {
					slog.Warn("Failed to add TAP firewall rules during respawn", "tap", v.TapDevice, "error", err)
				}
			})
			// TAP device creation outside batch (device is idempotent).
			if err := op.Services.Network.EnsureTapDevice(ctx, v.TapDevice, v.Network.Bridge); err != nil {
				slog.Warn("Failed to ensure TAP device during respawn", "tap", v.TapDevice, "error", err)
			}
		}
		libnet.FlushARP(ctx, v.Network.Bridge)
	}
	logLevel, _ := op.Services.Config.GetString(ctx, "defaults.firecracker", "log_level")
	fcPIDFilename, _ := op.Services.Config.GetString(ctx, "defaults.firecracker", "pid_filename")
	fcConfig := &model.FirecrackerConfig{
		VMDir:          vmDir,
		RootfsPath:     v.RootfsPath,
		BinaryPath:     v.Binary.Path,
		KernelPath:     v.Kernel.Path,
		VCPUCount:      v.VCPUCount,
		MemSizeMiB:     v.MemSizeMiB,
		GuestIP:        v.IPv4,
		GuestMAC:       v.MAC,
		TapName:        v.TapDevice,
		NetworkGateway: v.Network.IPv4Gateway,
		PCIEnabled:     v.PCIEnabled,
		NestedVirt:     v.NestedVirt,
		EnableConsole:  v.EnableConsole,
		EnableLogging:  v.EnableLogging,
		EnableMetrics:  v.EnableMetrics,
		BootArgs:       v.BootArgs,
		LSMFlags:       v.LSMFlags,
		SnapshotMode:   snapshotMode,
		ImageFSUUID:    v.Image.FSUUID,
		ImageFSType:    v.Image.FSType,
		// Full paths from DB (field names match DB column names)
		ConfigPath:       v.ConfigPath,
		APISocketPath:    v.APISocketPath,
		LogPath:          infra.DerefOrZero(v.LogPath),
		SerialOutputPath: infra.DerefOrZero(v.SerialOutputPath),
		LogLevel:         logLevel,
		PIDPath:          filepath.Join(vmDir, fcPIDFilename),
	}
	// --- Vsock config ---
	if v.Vsock != nil {
		fcConfig.Vsock = &model.VsockConfig{
			GuestCID: v.Vsock.GuestCID,
			UDSPath:  v.Vsock.UDSPath,
		}
	}
	// --- Attach volumes (extra drives) ---
	if len(v.Volumes) > 0 {
		fcConfig.ExtraDrives = volume.VolumesToDrives(v.Volumes, false)
	}
	// --- Console relay setup (before spawn) ---
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
	// --- Write config and spawn ---
	spawner := vm.NewFirecrackerSpawner(fcConfig)
	if err := spawner.WriteToFile(); err != nil {
		return fmt.Errorf("write firecracker config: %w", err)
	}
	if err := spawner.Spawn(); err != nil {
		slog.Warn("Failed to respawn Firecracker", "vm", v.Name, "error", err)
		return err
	}
	// --- Start console relay after spawn ---
	if consoleController != nil {
		consoleController.CloseClientFD()
		_, _, startErr := consoleController.Start(ctx)
		if startErr != nil {
			slog.Warn("Console relay start failed during respawn", "vm", v.Name, "error", startErr)
		} else {
			slog.Info("Console relay started for VM", "vm", v.Name, "socket", consoleController.SocketPath())
		}
	}
	// --- Update DB and in-memory VM object ---
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
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
		// Enrichment is best-effort: enriched data improves inspect output but is not
		// required for the operation to succeed. A failed enrichment should not fail
		// the operation itself.
	}
	for _, vmLocal := range vms {
		// Stop the VM first (kills the firecracker process)
		controller := vm.NewController(vmLocal, repo)
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

// --- AttachVolume / DetachVolume ---
// AttachVolume attaches a volume to a VM.
func (op *Operation) VMAttachVolume(
	ctx context.Context,
	input inputs.VMInput,
	volumeName string,
) error {
	if err := system.CheckPrivileges("/usr/sbin/ip", "attach volume"); err != nil {
		return errs.WrapMsg(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("Privilege check failed: %v", err),
			err,
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}
	vms, resolveErr := input.Resolve(ctx, op.Repos.VM)
	if resolveErr != nil {
		return errs.NotFound(errs.CodeVMNotFound, fmt.Sprintf("VM not found: %v", resolveErr))
	}
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
	}
	if len(vms) != 1 {
		return errs.NotFound(errs.CodeVMNotFound, "Expected exactly one VM identifier")
	}
	vmItem := vms[0]
	// Resolve volume using VolumeResolver.
	volResolver := volume.NewResolver(op.Repos.Volume)
	vol, err := volResolver.Resolve(ctx, volumeName)
	if err != nil {
		return errs.NotFound(errs.CodeVolumeNotFound, fmt.Sprintf("Volume '%s' not found", volumeName))
	}
	if vol.Status == model.VolumeStatusAttached {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf("Volume '%s' is already attached", volumeName),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// Check volume status.
	// Shareable read-only volumes are always attachable regardless of status.
	if vol.Status != model.VolumeStatusAvailable && !(vol.IsShareable && vol.IsReadOnly) {
		return errs.New(
			errs.CodeVMCreateFailed,
			fmt.Sprintf("Volume '%s' is not available", volumeName),
			errs.WithClass(errs.ClassValidation),
		)
	}
	// Hotplug on running VM
	if vmItem.Status == model.VMStatusRunning {
		if !vmItem.PCIEnabled {
			return errs.New(
				errs.CodeVMCreateFailed,
				fmt.Sprintf(
					"PCI is not enabled for VM '%s' — volume hotplug requires PCI access in the guest",
					vmItem.Name,
				),
				errs.WithClass(errs.ClassValidation),
			)
		}
		// Version gate: hotplug requires Firecracker v1.16+
		if vmItem.BinaryID != "" {
			bin, _ := op.Repos.Binary.Get(ctx, vmItem.BinaryID)
			if bin != nil && bin.Version != "" {
				if !version.IsAtLeastFor(bin.Version, version.FeatureHotplug) {
					return errs.New(
						errs.CodeBinaryVersionGate,
						fmt.Sprintf(
							"Volume hotplug requires Firecracker >= 1.16, got %s. "+
								"Use a newer Firecracker binary or attach the volume while the VM is stopped.",
							bin.Version,
						),
					)
				}
			}
		}
		// Attempt hotplug via Firecracker API.
		controller := vm.NewController(vmItem, op.Repos.VM)
		if err := controller.AttachVolume(ctx, model.DriveConfig{
			DriveID:      vol.ID,
			PathOnHost:   vol.Path,
			IsRootDevice: false,
			IsReadOnly:   vol.IsReadOnly,
			CacheType:    "Unsafe",
			IOEngine:     "Sync",
		}); err != nil {
			return errs.New(
				errs.CodeFirecrackerClientError,
				fmt.Sprintf("Hotplug failed: %v", err),
			)
		}
		client, err := op.vsockClient(ctx, vmItem)
		if err != nil {
			slog.Warn("vsock client not available for PCI rescan, device may not appear until reboot",
				"vm", vmItem.Name, "volume", vol.Name, "error", err)
		}
		if client != nil {
			if err := client.RescanPCI(ctx); err != nil {
				slog.Warn("guest PCI rescan via vsock failed, device may not appear until reboot",
					"vm", vmItem.Name, "volume", vol.Name, "error", err)
			}
		}
	}
	// VolumeController.Attach
	volController := volume.NewController(vol, op.Repos.Volume)
	if err := volController.Attach(ctx, vmItem.ID); err != nil {
		slog.Warn("failed to attach volume to VM", "vm", vmItem.Name, "volume", vol.Name, "error", err)
	}
	// Update VM's volume_ids
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
	if err := op.Repos.VM.Upsert(ctx, vmItem); err != nil {
		slog.Warn("failed to update VM volume IDs", "vm", vmItem.Name, "error", err)
	}
	op.AuditLog.LogOperation("vm.attach_volume", map[string]any{
		"vm": vmItem.Name, "volume": vol.Name,
	}, "")
	return nil
}

// DetachVolume detaches a volume from a VM.
// - VMInput for identification (name, ID, IP, MAC)
// - VolumeResolver for volume resolution
// - Version gate + vsock PCI removal + Firecracker API for hot-unplug
// - VolumeController.detach + VM volume_ids update
func (op *Operation) VMDetachVolume(
	ctx context.Context,
	input inputs.VMInput,
	volumeName string,
) error {
	if err := system.CheckPrivileges("/usr/sbin/ip", "detach volume"); err != nil {
		return errs.WrapMsg(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("Privilege check failed: %v", err),
			err,
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}
	vms, resolveErr := input.Resolve(ctx, op.Repos.VM)
	if resolveErr != nil {
		return errs.NotFound(errs.CodeVMNotFound, fmt.Sprintf("VM not found: %v", resolveErr))
	}
	if op.Enr != nil && len(vms) > 0 {
		_ = op.Enr.EnrichVM(ctx, vms, "kernel", "image", "binary", "network", "network.leases", "volumes")
	}
	if len(vms) != 1 {
		return errs.NotFound(errs.CodeVMNotFound, "Expected exactly one VM identifier")
	}
	vmItem := vms[0]
	volResolver := volume.NewResolver(op.Repos.Volume)
	vol, err := volResolver.Resolve(ctx, volumeName)
	if err != nil {
		return errs.NotFound(errs.CodeVolumeNotFound, fmt.Sprintf("Volume '%s' not found", volumeName))
	}
	// Hot-unplug if running
	if vmItem.Status == model.VMStatusRunning {
		// Version gate: hot-unplug requires Firecracker v1.16+
		if vmItem.BinaryID != "" {
			bin, _ := op.Repos.Binary.Get(ctx, vmItem.BinaryID)
			if bin != nil && bin.Version != "" {
				if !version.IsAtLeastFor(bin.Version, version.FeatureHotUnplug) {
					return errs.New(
						errs.CodeBinaryVersionGate,
						fmt.Sprintf(
							"Volume hot-unplug requires Firecracker >= 1.16, got %s. "+
								"Use a newer Firecracker binary or detach the volume while the VM is stopped.",
							bin.Version,
						),
					)
				}
			}
		}
		client, err := op.vsockClient(ctx, vmItem)
		if err != nil {
			slog.Warn("vsock client not available for PCI device removal",
				"vm", vmItem.Name, "volume", vol.Name, "error", err)
		}
		if client != nil {
			if err := client.RemoveHotpluggedPCIDevice(ctx); err != nil {
				slog.Warn("failed to remove PCI device from guest via sysfs",
					"vm", vmItem.Name, "volume", vol.Name, "error", err)
			}
		}

		// Attempt hot-unplug via Firecracker API.
		ctrl := vm.NewController(vmItem, op.Repos.VM)
		if err := ctrl.DetachVolume(ctx, vol.ID); err != nil {
			slog.Warn("Hot-unplug failed for drive", "volume", vol.ID, "error", err)
		}

		// Post-detach PCI rescan so the guest kernel reclaims the device slot.
		if client != nil {
			if err := client.RescanPCI(ctx); err != nil {
				slog.Warn("guest PCI rescan after hot-unplug failed",
					"vm", vmItem.Name, "volume", vol.Name, "error", err)
			}
		}
	}
	// VolumeController.Detach
	volController := volume.NewController(vol, op.Repos.Volume)
	if err := volController.Detach(ctx); err != nil {
		slog.Warn("failed to detach volume from VM", "vm", vmItem.Name, "volume", vol.Name, "error", err)
	}
	// Update VM's volume_ids
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
	if err := op.Repos.VM.Upsert(ctx, vmItem); err != nil {
		slog.Warn("failed to update VM volume IDs", "vm", vmItem.Name, "error", err)
	}
	op.AuditLog.LogOperation("vm.detach_volume", map[string]any{
		"vm": vmItem.Name, "volume": vol.Name,
	}, "")
	return nil
}

// --- Exec ---
// VMExec executes a command inside a VM via the vsock guest agent.
// If input.Command is empty, opens an interactive PTY shell session.
// For non-interactive execution, output is captured and returned as structured result.
// For interactive shell, I/O is connected directly to the terminal and no result is returned.
func (op *Operation) VMExec(ctx context.Context, input inputs.VMExecInput) (*results.VMExecResult, error) {
	resolved, err := input.Resolve(ctx, op.Repos.VM, op.Repos.Vsock)
	if err != nil {
		return nil, err
	}
	// Read probe timeout from config (defaults.vm.vsock_probe_timeout in constants.go).
	probeTimeout, err := op.Services.Config.GetDuration(ctx, "defaults.vm", "vsock_probe_timeout")
	if err != nil || probeTimeout <= 0 {
		return nil, errs.New(
			errs.CodeInternal,
			"vsock_probe_timeout not configured — check defaults.vm.vsock_probe_timeout",
		)
	}
	client, err := op.newVsockClient(ctx, resolved.VsockItem, probeTimeout, resolved.VM.Name)
	if err != nil {
		return nil, err
	}
	// Interactive shell or captured exec
	if input.Command == "" {
		// Interactive shell session — no result returned since I/O is direct to terminal.
		if err := client.Shell(ctx, input.User); err != nil {
			return nil, errs.WrapMsg(
				errs.CodeVsockExecFailed,
				fmt.Sprintf("vsock shell session failed for vm '%s'", resolved.VM.Name),
				err,
			)
		}
		return nil, nil
	}
	user := resolved.User
	if user == "" {
		user, _ = op.Services.Config.GetString(ctx, "defaults.vm", "vsock_user")
	}
	result, err := client.Exec(ctx, input.Command, user, input.Timeout, input.Env, input.NoSync)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeVsockExecFailed,
			fmt.Sprintf("vsock exec failed for vm '%s'", resolved.VM.Name),
			err,
		)
	}
	return &results.VMExecResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}, nil
}

// --- Builder ---
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
		VMDir:           c.vmDir,
		RootfsPath:      c.rootfsPath,
		BinaryPath:      c.resolved.Binary.Path,
		KernelPath:      c.resolved.Kernel.Path,
		VCPUCount:       c.resolved.VCPUCount,
		MemSizeMiB:      c.resolved.MemSizeMib,
		GuestIP:         c.guestIP,
		GuestMAC:        c.guestMAC,
		TapName:         c.tapName,
		NetworkGateway:  c.resolved.Network.IPv4Gateway,
		NetworkNetmask:  c.resolved.NetworkNetmask,
		ImageFSUUID:     c.resolved.Image.FSUUID,
		ImageFSType:     c.resolved.Image.FSType,
		BootArgs:        c.resolved.BootArgs,
		LSMFlags:        c.resolved.LSMFlags,
		PCIEnabled:      c.resolved.PCIEnabled,
		NestedVirt:      c.resolved.NestedVirt,
		CPUVendor:       cpuVendor,
		CPUArchitecture: cpuArchitecture,
		CloudInitMode:   &ciMode,
		EnableConsole:   c.resolved.EnableConsole,
		EnableLogging:   c.resolved.EnableLogging,
		EnableMetrics:   c.resolved.EnableMetrics,
		LogLevel:        c.resolved.LogLevel,
		ExtraDrives:     c.resolved.ExtraDrives,
		Writeback:       c.resolved.Writeback,
		// Full paths constructed from VMDir + resolved filenames.
		// Field names match DB column names for respawn compatibility.
		ConfigPath:       filepath.Join(c.vmDir, c.resolved.ConfigFilename),
		APISocketPath:    filepath.Join(c.vmDir, c.resolved.APISocketFilename),
		LogPath:          filepath.Join(c.vmDir, c.resolved.LogFilename),
		SerialOutputPath: filepath.Join(c.vmDir, c.resolved.SerialOutputFilename),
		MetricsPath:      filepath.Join(c.vmDir, c.resolved.MetricsFilename),
		PIDPath:          filepath.Join(c.vmDir, c.resolved.PIDFilename),
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
	if c.vsockCID > 0 && c.vsockUDSPath != "" {
		fcConfig.Vsock = &model.VsockConfig{
			GuestCID: c.vsockCID,
			UDSPath:  c.vsockUDSPath,
		}
	}
	return fcConfig
}
func (c *VMCreateBuilder) toVMModel() *model.VMItem {
	if c.resolved == nil || c.spawner == nil || c.spawner.PID == nil {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	logPath := filepath.Join(c.vmDir, c.resolved.LogFilename)
	serialPath := filepath.Join(c.vmDir, c.resolved.SerialOutputFilename)
	vm := &model.VMItem{
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
