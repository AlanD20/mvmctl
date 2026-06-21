package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/firecracker"
	"mvmctl/internal/lib/model"
	libnet "mvmctl/internal/lib/network"
	"mvmctl/internal/lib/provisioner"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// SnapshotAPI defines the public interface for snapshot operations.
type SnapshotAPI interface {
	SnapshotCreate(
		ctx context.Context,
		input inputs.SnapshotCreateInput,
		onProgress event.OnProgressCallback,
	) (*model.SnapshotItem, error)
	SnapshotList(ctx context.Context) []*model.SnapshotItem
	SnapshotInspect(ctx context.Context, input inputs.SnapshotInput) (*results.SnapshotInspect, error)
	SnapshotRestore(ctx context.Context, input inputs.SnapshotRestoreInput) ([]*model.VMItem, error)
	SnapshotRemove(ctx context.Context, input inputs.SnapshotInput) *errs.BatchResult
}

// --- SnapshotCreate ---

// SnapshotCreate creates a snapshot from a running VM.
func (op *Operation) SnapshotCreate(
	ctx context.Context,
	input inputs.SnapshotCreateInput,
	onProgress event.OnProgressCallback,
) (*model.SnapshotItem, error) {
	if err := input.Validate(); err != nil {
		return nil, errs.New(errs.CodeSnapshotCreateFailed, err.Error())
	}
	if err := system.CheckPrivileges("/usr/sbin/ip", "create snapshot"); err != nil {
		return nil, errs.WrapMsg(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("privilege check failed: %v", err),
			err,
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}

	// 1. Resolve VM
	emitProgress(onProgress, "resolve", "running", "Resolving VM...")
	vmItem, err := input.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("vm not found: %s", input.Identifier), err)
	}

	// 2. Enrich VM with needed relations
	emitProgress(onProgress, "enrich", "running", "Enriching VM info...")
	if err := op.Enr.EnrichVM(ctx, []*model.VMItem{vmItem}, "kernel", "image", "binary", "network", "vsock"); err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			"failed to enrich VM with relations", err)
	}

	// 3. Generate snapshot ID
	createdAt := infra.Now()
	snapID := crypto.SnapshotID(vmItem.ID, createdAt)

	// 4. Create snapshot directory
	snapDir := infra.GetSnapshotDir(snapID)
	emitProgress(onProgress, "dir", "running", fmt.Sprintf("Creating snapshot directory: %s", snapDir))
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("failed to create snapshot directory %s", snapDir), err)
	}

	// Cleanup on any failure
	var cleanup bool
	defer func() {
		if cleanup {
			os.RemoveAll(snapDir)
		}
	}()

	// 5. Copy rootfs
	rootfsFile := filepath.Join(snapDir, "rootfs.ext4")
	emitProgress(onProgress, "rootfs", "running", "Copying root filesystem...")
	if err := infra.CopyFile(vmItem.RootfsPath, rootfsFile); err != nil {
		cleanup = true
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("failed to copy rootfs from %s: %v", vmItem.RootfsPath, err), err)
	}

	// 6. Firecracker API operations
	memFile := filepath.Join(snapDir, "memory")
	stateFile := filepath.Join(snapDir, "vmstate")
	wasRunning := vmItem.Status == model.VMStatusRunning

	if vmItem.APISocketPath != "" {
		client := firecracker.NewClient(vmItem.APISocketPath)
		defer client.Close()

		// 6a. Pause VM if running
		if wasRunning {
			emitProgress(onProgress, "pause", "running", "Pausing VM...")
			if err := client.PauseVM(ctx); err != nil {
				cleanup = true
				return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
					fmt.Sprintf("failed to pause VM '%s': %v", vmItem.Name, err), err)
			}
			if err := op.Repos.VM.UpdateStatus(ctx, vmItem.ID, model.VMStatusPaused); err != nil {
				slog.Error("failed to update VM status to paused", "vm", vmItem.Name, "error", err)
			}
			vmItem.Status = model.VMStatusPaused
		}

		// 6b. Create snapshot
		emitProgress(onProgress, "snapshot", "running", "Creating Firecracker snapshot...")
		if _, err := client.CreateSnapshot(ctx, memFile, stateFile); err != nil {
			// If we paused the VM, try to resume it before returning
			if wasRunning {
				if resumeErr := client.ResumeVM(ctx); resumeErr != nil {
					slog.Error("failed to resume VM after snapshot failure",
						"vm", vmItem.Name, "error", resumeErr)
				}
				if statusErr := op.Repos.VM.UpdateStatus(ctx, vmItem.ID, model.VMStatusRunning); statusErr != nil {
					slog.Error("failed to update VM status to running after snapshot failure",
						"vm", vmItem.Name, "error", statusErr)
				}
			}
			cleanup = true
			return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
				fmt.Sprintf("failed to create snapshot for VM '%s': %v", vmItem.Name, err), err)
		}

		// 6c. Resume VM if not --pause
		if wasRunning && !input.Pause {
			emitProgress(onProgress, "resume", "running", "Resuming VM...")
			if err := client.ResumeVM(ctx); err != nil {
				slog.Error("failed to resume VM after snapshot",
					"vm", vmItem.Name, "error", err)
			}
			if err := op.Repos.VM.UpdateStatus(ctx, vmItem.ID, model.VMStatusRunning); err != nil {
				slog.Error("failed to update VM status to running", "vm", vmItem.Name, "error", err)
			}
			vmItem.Status = model.VMStatusRunning
		}
	} else {
		// No API socket — snapshot from stopped VM (just copy rootfs)
		emitProgress(onProgress, "snapshot", "running", "No API socket — snapshot from stopped VM (rootfs only)...")
		emitProgress(onProgress, "info", "running",
			"Snapshot from stopped VM: no memory/state files captured")
	}

	// 7. Construct snapshot name (default: <vm>-<timestamp>)
	snapName := vmItem.Name + "-" + createdAt
	if input.Name != nil && *input.Name != "" {
		snapName = *input.Name
	}

	// 8. Build extra config from VM boot settings
	var extraConfig *model.SnapshotExtraConfig
	if vmItem.BootArgs != "" || vmItem.LSMFlags != "" || vmItem.CPUConfig != nil || vmItem.Vsock != nil {
		extraConfig = &model.SnapshotExtraConfig{
			BootArgs:      vmItem.BootArgs,
			LSMFlags:      vmItem.LSMFlags,
			PCIEnabled:    vmItem.PCIEnabled,
			NestedVirt:    vmItem.NestedVirt,
			Console:       vmItem.EnableConsole,
			EnableLogging: vmItem.EnableLogging,
			EnableMetrics: vmItem.EnableMetrics,
			LogLevel:      vmItem.LogLevel,
			CPUConfig:     vmItem.CPUConfig,
		}
		if vmItem.Vsock != nil {
			extraConfig.VsockPort = vmItem.Vsock.Port
			extraConfig.VsockCID = vmItem.Vsock.GuestCID
			extraConfig.VsockToken = vmItem.Vsock.Token
		}
	}

	// 9. Build metadata from enriched VM
	now := infra.Now()
	snapshotItem := &model.SnapshotItem{
		ID:           snapID,
		Name:         snapName,
		SourceVMID:   vmItem.ID,
		SourceVMName: vmItem.Name,
		SnapshotDir:  snapDir,
		MemoryFile:   memFile,
		StateFile:    stateFile,
		RootfsFile:   rootfsFile,
		ImageID:      vmItem.ImageID,
		KernelID:     vmItem.KernelID,
		NetworkID:    vmItem.NetworkID,
		BinaryID:     vmItem.BinaryID,
		VCPUCount:    vmItem.VCPUCount,
		MemSizeMiB:   vmItem.MemSizeMiB,
		DiskSizeMiB:  vmItem.DiskSizeMiB,
		SSHKeys:      vmItem.SSHKeys,
		SSHUser:      vmItem.SSHUser,
		ExtraConfig:  extraConfig,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// 9. Persist to DB
	emitProgress(onProgress, "store", "running", "Persisting snapshot metadata...")
	if err := op.Repos.Snapshot.Upsert(ctx, snapshotItem); err != nil {
		cleanup = true
		return nil, errs.WrapMsg(errs.CodeSnapshotCreateFailed,
			fmt.Sprintf("failed to persist snapshot record: %v", err), err)
	}

	// Cleanup is no longer needed on success
	cleanup = false

	op.AuditLog.LogOperation("snapshot.create", map[string]any{
		"snapshot_id": snapID,
		"source_vm":   vmItem.Name,
		"name":        snapName,
	}, "")

	emitProgress(onProgress, "complete", "complete", fmt.Sprintf("Snapshot created: %s", snapID))
	return snapshotItem, nil
}

// --- SnapshotRestore ---

// SnapshotRestore restores one or more VMs from a snapshot.
func (op *Operation) SnapshotRestore(
	ctx context.Context,
	input inputs.SnapshotRestoreInput,
) ([]*model.VMItem, error) {
	if err := system.CheckPrivileges("/usr/sbin/ip", "restore snapshot"); err != nil {
		return nil, errs.WrapMsg(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("privilege check failed: %v", err),
			err,
			errs.WithClass(errs.ClassNeedsInteraction),
		)
	}

	// 1. Resolve snapshot by prefix
	snap, err := input.ResolveSnapshot(ctx, op.Repos.Snapshot)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
			fmt.Sprintf("snapshot not found: %s", input.SnapshotID), err)
	}

	// 2. Enrich snapshot with relations
	if err := op.Enr.EnrichSnapshot(
		ctx,
		[]*model.SnapshotItem{snap},
		"image",
		"kernel",
		"network",
		"binary",
	); err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
			"failed to enrich snapshot with relations", err)
	}

	// 3. Determine network to use
	networkItem, err := input.ResolveNetwork(ctx, op.Repos.Network)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
			fmt.Sprintf("network not found: %s", *input.Network), err)
	}
	if networkItem == nil {
		// Fallback to snapshot's original network (from enrichment)
		networkItem = snap.Network
	}

	if networkItem == nil {
		return nil, errs.New(errs.CodeSnapshotRestoreFailed,
			"no network available for snapshot restore. specify --network or ensure snapshot has a network")
	}

	// 4. Enrich network with leases
	if err := op.Enr.EnrichNetwork(ctx, []*model.NetworkItem{networkItem}, "leases"); err != nil {
		slog.Warn("Failed to enrich network with leases", "network", networkItem.Name, "error", err)
	}

	// 5. Generate VM names and resolve kernel/binary
	count := input.Count
	if count <= 0 {
		count = 1
	}

	// Resolve default MAC prefix from config (always set via OverridableDefaults)
	guestMACPrefix, _ := op.Services.Config.GetString(ctx, "defaults.vm", "guest_mac_prefix")

	names := vm.GenerateBatchNames(input.Name, count)

	// Pre-check name collisions
	existing, err := op.Repos.VM.NamesExist(ctx, names)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
			fmt.Sprintf("failed to check name collisions: %v", err), err)
	}
	if len(existing) > 0 {
		return nil, errs.New(errs.CodeSnapshotRestoreFailed,
			fmt.Sprintf("vm name(s) already exist: %s", strings.Join(existing, ", ")))
	}

	// 6. Bridge + TAP setup (shared batch)
	bridgeAddr, calcErr := network.ComputeBridgeAddress(networkItem.IPv4Gateway, networkItem.Subnet)
	if calcErr != nil {
		return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
			"failed to compute bridge address", calcErr)
	}

	natGateways := network.NatGatewaysList(networkItem)
	tapNames := make([]string, len(names))
	for i, name := range names {
		tapNames[i] = libnet.VMGenerateTAPName(networkItem.Name, name)
	}

	op.Services.Network.WithBatch(ctx, func() {
		if err := op.Services.Network.EnsureBridge(ctx, networkItem.Bridge, bridgeAddr); err != nil {
			slog.Error("Failed to ensure bridge during snapshot restore", "bridge", networkItem.Bridge, "error", err)
			return
		}
		if networkItem.NATEnabled && len(natGateways) > 0 {
			if natErr := op.Services.Network.EnsureNAT(ctx, networkItem.Bridge,
				natGateways, networkItem.Subnet, networkItem.ID); natErr != nil {
				slog.Warn("Failed to ensure NAT rules during snapshot restore",
					"network", networkItem.Name, "error", natErr)
			}
		}
		for _, tapName := range tapNames {
			if err := op.Services.Network.AddTapFirewallRules(ctx, tapName,
				networkItem.Bridge, networkItem.ID, networkItem.Subnet); err != nil {
				slog.Warn("Failed to add TAP firewall rules during snapshot restore",
					"tap", tapName, "error", err)
			}
		}
	})

	// 7. Resolve rootfs suffix from image
	rootfsSuffix := "ext4"
	if snap.ImageID != "" {
		img, imgErr := op.Repos.Image.Get(ctx, snap.ImageID)
		if imgErr != nil {
			return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
				fmt.Sprintf("failed to resolve image %s: %v", snap.ImageID, imgErr), imgErr)
		}
		if img == nil {
			return nil, errs.New(errs.CodeSnapshotRestoreFailed,
				fmt.Sprintf("image %s referenced by snapshot not found", snap.ImageID))
		}
		rootfsSuffix = img.FSType
	}

	// 8. Create VMs
	var createdVMs []*model.VMItem
	for i, name := range names {
		createdAt := time.Now()
		vmID := crypto.VMID(name, createdAt.Format(time.RFC3339))
		vmDir := infra.GetVMDirByID(vmID)

		// Create VM directory
		if err := os.MkdirAll(vmDir, 0755); err != nil {
			// On failure, clean up previously created VMs
			for _, cvm := range createdVMs {
				op.cleanupRestoredVM(ctx, cvm)
			}
			return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
				fmt.Sprintf("failed to create VM directory %s: %v", vmDir, err), err)
		}

		// Copy rootfs from snapshot
		rootfsPath := filepath.Join(vmDir, "rootfs.ext4")
		if err := infra.CopyFile(snap.RootfsFile, rootfsPath); err != nil {
			for _, cvm := range createdVMs {
				op.cleanupRestoredVM(ctx, cvm)
			}
			return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
				fmt.Sprintf("failed to copy rootfs for VM '%s': %v", name, err), err)
		}

		// Allocate IP and create TAP
		leaseCtrl, leaseErr := network.NewLeaseController(ctx, networkItem, op.Repos.Lease, nil)
		if leaseErr != nil {
			for _, cvm := range createdVMs {
				op.cleanupRestoredVM(ctx, cvm)
			}
			return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
				fmt.Sprintf("failed to create lease controller: %v", leaseErr), leaseErr)
		}

		guestIP, leaseErr := leaseCtrl.Lease(ctx, vmID)
		if leaseErr != nil {
			for _, cvm := range createdVMs {
				op.cleanupRestoredVM(ctx, cvm)
			}
			return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
				fmt.Sprintf("failed to lease IP for VM '%s': %v", name, leaseErr), leaseErr)
		}

		guestMAC := libnet.VMGenerateMAC(guestMACPrefix)
		tapName := tapNames[i]

		if err := op.Services.Network.EnsureTapDevice(ctx, tapName, networkItem.Bridge); err != nil {
			for _, cvm := range createdVMs {
				op.cleanupRestoredVM(ctx, cvm)
			}
			return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
				fmt.Sprintf("failed to create TAP device for VM '%s': %v", name, err), err)
		}

		// Create VM record
		vmItem := &model.VMItem{
			ID:           vmID,
			Name:         name,
			Status:       model.VMStatusStopped,
			IPv4:         guestIP,
			MAC:          guestMAC,
			NetworkID:    networkItem.ID,
			TapDevice:    tapName,
			KernelID:     snap.KernelID,
			BinaryID:     snap.BinaryID,
			VCPUCount:    snap.VCPUCount,
			MemSizeMiB:   snap.MemSizeMiB,
			DiskSizeMiB:  snap.DiskSizeMiB,
			RootfsPath:   rootfsPath,
			RootfsSuffix: rootfsSuffix,
			ImageID:      snap.ImageID,
			CreatedAt:    createdAt.Format(time.RFC3339),
			UpdatedAt:    createdAt.Format(time.RFC3339),
			SSHKeys:      snap.SSHKeys,
			SSHUser:      snap.SSHUser,
		}

		// Apply preserved extra config from snapshot
		if cfg := snap.ExtraConfig; cfg != nil {
			vmItem.BootArgs = cfg.BootArgs
			vmItem.LSMFlags = cfg.LSMFlags
			vmItem.PCIEnabled = cfg.PCIEnabled
			vmItem.NestedVirt = cfg.NestedVirt
			vmItem.EnableConsole = cfg.Console
			vmItem.EnableLogging = cfg.EnableLogging
			vmItem.EnableMetrics = cfg.EnableMetrics
			vmItem.LogLevel = cfg.LogLevel
		}

		// Persist VM record
		if err := op.Repos.VM.Upsert(ctx, vmItem); err != nil {
			for _, cvm := range createdVMs {
				op.cleanupRestoredVM(ctx, cvm)
			}
			return nil, errs.WrapMsg(errs.CodeSnapshotRestoreFailed,
				fmt.Sprintf("failed to persist VM record '%s': %v", name, err), err)
		}

		// Enrich VM with relations needed for respawn
		if err := op.Enr.EnrichVM(ctx, []*model.VMItem{vmItem},
			"kernel", "image", "binary", "network"); err != nil {
			slog.Warn("Failed to enrich restored VM", "vm", name, "error", err)
		}

		// Set image fallback by re-fetching from enriched kernel
		if snap.Kernel != nil {
			vmItem.Kernel = snap.Kernel
		}
		if snap.Binary != nil {
			vmItem.Binary = snap.Binary
		}
		vmItem.Network = networkItem

		// --- Vsock setup for restored VM ---
		// Use the same port and token from the snapshot (matches the frozen
		// guest agent). Allocate a fresh CID to avoid conflicts with the
		// source VM (which is still running with the original CID).
		// Only the UDS path changes (new VM directory).
		if snap.ExtraConfig != nil && snap.ExtraConfig.VsockPort > 0 && vmItem.RootfsPath != "" {
			vsockFilename, _ := op.Services.Config.GetString(ctx, "defaults.firecracker", "vsock_filename")
			vsockUDSPath := filepath.Join(vmDir, vsockFilename)

			// Allocate fresh CID — the snapshot's CID is already in use
			// by the source VM (t2-base) which is still running.
			vsockCID, cidErr := op.Services.Vsock.AllocateCID()
			if cidErr != nil {
				slog.Error("Failed to allocate vsock CID for restored VM",
					"vm", name, "error", cidErr)
			} else {
				// Re-inject vsock agent into the copied rootfs with the
				// saved token, so host and guest stay in sync.
				if agentBin := vsock.AgentBinary(); len(agentBin) > 0 {
					rootUID, _ := op.Services.Config.GetInt(ctx, "defaults.vm", "root_uid")
					rootGID, _ := op.Services.Config.GetInt(ctx, "defaults.vm", "root_gid")
					userUID, _ := op.Services.Config.GetInt(ctx, "defaults.vm", "user_uid")
					userGID, _ := op.Services.Config.GetInt(ctx, "defaults.vm", "user_gid")

					backend, beErr := provisioner.NewBackend(ctx, provisioner.BackendOpts{
						RootfsPath:      vmItem.RootfsPath,
						FsType:          rootfsSuffix,
						CacheDir:        op.CacheDir,
						ProvisionerType: provisioner.ProvisionerType(op.ProvisionerType),
						RootUID:         rootUID,
						RootGID:         rootGID,
						UserUID:         userUID,
						UserGID:         userGID,
					})
					if beErr != nil {
						slog.Error("Failed to create provisioner backend for vsock injection",
							"vm", name, "error", beErr)
					} else {
						if injErr := backend.InjectVsockAgent(ctx, agentBin, snap.ExtraConfig.VsockPort, snap.ExtraConfig.VsockToken); injErr != nil {
							slog.Error("Failed to inject vsock agent into restored rootfs",
								"vm", name, "error", injErr)
						} else if runErr := backend.Run(ctx); runErr != nil {
							slog.Error("Failed to run provisioner for vsock agent injection",
								"vm", name, "error", runErr)
						}
					}
				}

				// Persist vsock config with fresh CID but same port + token.
				if persistErr := op.Services.Vsock.PersistConfig(
					ctx, vsockCID, vmID, name, vsockUDSPath,
					snap.ExtraConfig.VsockPort, snap.ExtraConfig.VsockToken,
				); persistErr != nil {
					slog.Error("Failed to persist vsock config for restored VM",
						"vm", name, "error", persistErr)
				} else {
					vmItem.Vsock = &model.VsockConfigItem{
						GuestCID: vsockCID,
						UDSPath:  vsockUDSPath,
						Port:     snap.ExtraConfig.VsockPort,
						Token:    snap.ExtraConfig.VsockToken,
					}
				}
			}
		}

		// Set file paths for the new VM's directory
		configFilename, _ := op.Services.Config.GetString(ctx, "defaults.firecracker", "config_filename")
		apiSocketFilename, _ := op.Services.Config.GetString(ctx, "defaults.firecracker", "api_socket_filename")
		logFilename, _ := op.Services.Config.GetString(ctx, "defaults.firecracker", "log_filename")
		serialOutputFilename, _ := op.Services.Config.GetString(ctx, "defaults.firecracker", "serial_output_filename")
		vmItem.ConfigPath = filepath.Join(vmDir, configFilename)
		vmItem.APISocketPath = filepath.Join(vmDir, apiSocketFilename)
		logPath := filepath.Join(vmDir, logFilename)
		serialPath := filepath.Join(vmDir, serialOutputFilename)
		vmItem.LogPath = &logPath
		vmItem.SerialOutputPath = &serialPath

		// Persist the in-memory paths back to DB so the re-read
		// after respawn doesn't wipe them with stale empty values.
		_ = op.Repos.VM.Upsert(ctx, vmItem)

		// Respawn Firecracker in snapshot mode (stays paused)
		if err := op.vmRespawnFirecracker(ctx, vmItem, true); err != nil {
			// Don't rollback — VM record exists, just log error
			slog.Error("Failed to respawn Firecracker for snapshot restore",
				"vm", name, "error", err)
			// best-effort: mark as errored in DB so user can inspect
			_ = op.Repos.VM.UpdateStatus(ctx, vmItem.ID, model.VMStatusError)
			createdVMs = append(createdVMs, vmItem)
			continue
		}

		// Re-read updated VM from DB
		updated, getErr := op.Repos.VM.Get(ctx, vmItem.ID)
		if getErr == nil && updated != nil {
			vmItem = updated
		}

		// Load snapshot via Firecracker API
		if vmItem.APISocketPath != "" {
			fcClient := firecracker.NewClient(vmItem.APISocketPath)

			// The snapshot's vmstate file hardcodes the ORIGINAL VM's
			// rootfs path. We created a copy at vmItem.RootfsPath (new VM
			// directory), but Firecracker will look for the rootfs at the
			// old path. Create a symlink from old → new so the snapshot
			// can find the rootfs file.
			oldVMDir := infra.GetVMDirByID(snap.SourceVMID)
			oldRootfsPath := filepath.Join(oldVMDir, filepath.Base(vmItem.RootfsPath))
			if _, statErr := os.Stat(oldRootfsPath); os.IsNotExist(statErr) {
				if mkdirErr := os.MkdirAll(oldVMDir, 0755); mkdirErr == nil {
					if symlinkErr := os.Symlink(vmItem.RootfsPath, oldRootfsPath); symlinkErr != nil {
						slog.Warn("failed to create rootfs symlink for snapshot restore", "vm", name, "from", oldRootfsPath, "to", vmItem.RootfsPath, "error", symlinkErr)
					} else {
						defer os.RemoveAll(oldVMDir)
					}
				}
			}

			if _, loadErr := fcClient.LoadSnapshot(ctx, snap.MemoryFile, snap.StateFile, input.Resume); loadErr != nil {
				slog.Error("failed to load snapshot for VM", "vm", name, "error", loadErr)
				// best-effort: mark as errored in DB so user can inspect
				_ = op.Repos.VM.UpdateStatus(ctx, vmItem.ID, model.VMStatusError)
				fcClient.Close()
				createdVMs = append(createdVMs, vmItem)
				continue
			}

			// Reconfigure vsock device with the new CID and UDS path.
			// Snapshot mode doesn't pass --config-file, so the vsock
			// section from firecracker.json is ignored by Firecracker.
			// The device must be configured via the API after loading.
			if vmItem.Vsock != nil && vmItem.Vsock.GuestCID > 0 && vmItem.Vsock.UDSPath != "" {
				if vsockErr := fcClient.PutVsock(ctx, vmItem.Vsock.GuestCID, vmItem.Vsock.UDSPath); vsockErr != nil {
					slog.Warn("Failed to reconfigure vsock device after snapshot load",
						"vm", name, "error", vsockErr)
				}
			}

			fcClient.Close()
		}

		// Update status based on --resume
		if input.Resume {
			// best-effort: update status in DB, non-fatal if it fails
			_ = op.Repos.VM.UpdateStatus(ctx, vmItem.ID, model.VMStatusRunning)
			vmItem.Status = model.VMStatusRunning
		}

		op.AuditLog.LogOperation("snapshot.restore",
			map[string]any{"snapshot_id": snap.ID, "vm": name}, "")
		createdVMs = append(createdVMs, vmItem)
	}

	return createdVMs, nil
}

// cleanupRestoredVM cleans up a VM created during snapshot restore on failure.
// All errors are best-effort — we try to clean up as much as possible.
func (op *Operation) cleanupRestoredVM(ctx context.Context, v *model.VMItem) {
	if v.TapDevice != "" && v.NetworkID != "" {
		_ = op.Services.Network.RemoveTap(ctx, v.TapDevice, "", v.NetworkID) // cleanup
	}
	if v.ID != "" {
		leaseRepo := network.NewLeaseRepository(op.Connection.DB())
		_ = leaseRepo.ReleaseByVM(ctx, v.ID) // cleanup
		_ = op.Repos.VM.Delete(ctx, v.ID)    // cleanup
	}
	vmDir := infra.GetVMDirByID(v.ID)
	if vmDir != "" {
		_ = os.RemoveAll(vmDir) // cleanup
	}
}

// --- SnapshotList ---

// SnapshotList returns all snapshots.
// Never returns an error (matches VMList pattern) — empty slice on failure.
func (op *Operation) SnapshotList(ctx context.Context) []*model.SnapshotItem {
	items, err := op.Repos.Snapshot.ListAll(ctx)
	if err != nil {
		slog.Error("failed to list snapshots", "error", err)
		return []*model.SnapshotItem{}
	}
	if len(items) > 0 {
		// best-effort enrichment for display purposes
		_ = op.Enr.EnrichSnapshot(ctx, items, "image", "kernel", "network", "binary")
	}
	return items
}

// --- SnapshotInspect ---

// SnapshotInspect returns detailed information about a single snapshot.
func (op *Operation) SnapshotInspect(
	ctx context.Context,
	input inputs.SnapshotInput,
) (*results.SnapshotInspect, error) {
	snaps, err := input.Resolve(ctx, op.Repos.Snapshot)
	if err != nil {
		return nil, err
	}
	if len(snaps) != 1 {
		return nil, fmt.Errorf("expected exactly one snapshot identifier")
	}
	snap := snaps[0]
	// best-effort enrichment for display purposes
	_ = op.Enr.EnrichSnapshot(ctx, []*model.SnapshotItem{snap}, "image", "kernel", "network", "binary")

	return &results.SnapshotInspect{
		Snapshot: results.SnapshotItemInfo{
			ID:           snap.ID,
			Name:         snap.Name,
			SourceVMID:   snap.SourceVMID,
			SourceVMName: snap.SourceVMName,
			BaseDir:      snap.SnapshotDir,
			CreatedAt:    snap.CreatedAt,
		},
		Assets: results.SnapshotAssetsInfo{
			Image:   snap.Image,
			Kernel:  snap.Kernel,
			Network: snap.Network,
			Binary:  snap.Binary,
		},
		Resources: results.SnapshotResourcesInfo{
			VCPU: snap.VCPUCount,
			Mem:  snap.MemSizeMiB,
			Disk: snap.DiskSizeMiB,
		},
	}, nil
}

// --- SnapshotRemove ---

// SnapshotRemove removes one or more snapshots.
// Uses input.Resolve to resolve identifiers (name or ID prefix), then processes
// each resolved snapshot. Returns a BatchResult (matches VMRemove pattern).
func (op *Operation) SnapshotRemove(ctx context.Context, input inputs.SnapshotInput) *errs.BatchResult {
	snapshots, resolveErr := input.Resolve(ctx, op.Repos.Snapshot)

	results := make([]errs.OperationResult, 0, len(input.Identifiers))

	// Report unresolvable identifiers as batch errors
	if resolveErr != nil && len(snapshots) == 0 {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "error", Code: string(errs.CodeSnapshotRemoveFailed), Message: resolveErr.Error()},
			},
		}
	}

	for _, snap := range snapshots {
		// Remove files on disk
		if snap.SnapshotDir != "" {
			if err := os.RemoveAll(snap.SnapshotDir); err != nil {
				slog.Warn("failed to remove snapshot directory",
					"dir", snap.SnapshotDir, "error", err)
			}
		}

		// Remove from DB
		if err := op.Repos.Snapshot.Delete(ctx, snap.ID); err != nil {
			results = append(results, errs.OperationResult{
				Status: "error", Code: string(errs.CodeSnapshotRemoveFailed),
				Item:    snap,
				Message: fmt.Sprintf("failed to delete snapshot record: %v", err),
			})
			continue
		}

		op.AuditLog.LogOperation("snapshot.remove",
			map[string]any{"snapshot_id": snap.ID, "name": snap.Name}, "")
		results = append(results, errs.OperationResult{
			Status: "success", Code: "snapshot.removed",
			Item: snap, Message: fmt.Sprintf("removed snapshot: %s", snap.Name),
		})
	}
	return &errs.BatchResult{Items: results}
}
