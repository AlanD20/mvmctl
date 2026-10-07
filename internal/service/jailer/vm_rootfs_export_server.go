package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// VMRootfsExportServer owns protected source selection, pause/copy/restoration,
// and publication of one independent image-import copy through the root dispatcher.
type VMRootfsExportServer struct{ instances *instanceAuthority }

// NewVMRootfsExportServer constructs the fixed named privileged disk-copy receiver.
func NewVMRootfsExportServer() *VMRootfsExportServer {
	return &VMRootfsExportServer{instances: newInstanceAuthority()}
}

// Export holds source lifecycle/backing/control authority until state restoration.
// CRITICAL: caller paths and SQLite projections never select protected source bytes.
func (server *VMRootfsExportServer) Export(
	ctx context.Context, ownerUID, ownerGID uint32, request VMRootfsExportRequest,
) (_ VMRootfsExportResult, returnErr error) {
	finalState := "unknown"
	defer func() {
		if returnErr != nil {
			returnErr = vmRootfsExportError(request.VMID, finalState, returnErr)
		}
	}()
	if err := request.Validate(); err != nil {
		return VMRootfsExportResult{}, err
	}
	if ctx == nil {
		return VMRootfsExportResult{}, instanceValidationError("VM export context is required")
	}
	caller := instanceCaller{uid: ownerUID, gid: ownerGID}
	locked, err := server.instances.lockOwnedInstance(ctx, caller, request.VMID)
	if err != nil {
		return VMRootfsExportResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, locked.Release(context.WithoutCancel(ctx))) }()
	record := locked.record
	if (record.lifecycle == instanceLifecycleActive && record.launch == nil) ||
		(record.lifecycle != instanceLifecycleActive && record.lifecycle != instanceLifecycleCleaned) {
		return VMRootfsExportResult{}, instanceStateError(
			request.VMID,
			"VM export requires active or cleaned source authority",
		)
	}
	journal, err := openVMRootfsExportJournal(ctx, locked.directories, request.VMID, true)
	if err != nil {
		return VMRootfsExportResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, journal.close(context.WithoutCancel(ctx))) }()
	cache, err := pinSnapshotCache(ctx, caller, request.CacheRoot)
	if err != nil {
		return VMRootfsExportResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	var source vmLaunchDeviceAuthority
	var control *snapshotAPISession
	if record.lifecycle == instanceLifecycleActive {
		processes, err := reopenRecordedProcesses(ctx, realProcessRecoveryDeps(), record)
		if err != nil {
			return VMRootfsExportResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, processes.Release(context.WithoutCancel(ctx))) }()
		if _, err := verifyRecoveredActiveIdentity(ctx, realProcessIdentityVerifierDeps(), record); err != nil {
			return VMRootfsExportResult{}, err
		}
		if err := processes.verifyLocators(ctx); err != nil {
			return VMRootfsExportResult{}, err
		}
		root, err := pinSnapshotActiveRoot(ctx, record)
		if err != nil {
			return VMRootfsExportResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, unix.Close(root)) }()
		source, err = readVMLaunchDeviceAuthority(ctx, record, root)
		if err != nil {
			return VMRootfsExportResult{}, err
		}
		control, err = openSnapshotAPISession(ctx, record, processes)
		if err != nil {
			return VMRootfsExportResult{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, control.Release(context.WithoutCancel(ctx))) }()
	} else {
		source, err = readStoppedVMLaunchDeviceAuthority(ctx, record)
		if err != nil {
			return VMRootfsExportResult{}, err
		}
	}
	if source.Rootfs.ownerUID != ownerUID || source.Rootfs.kind != protectedBackingRootfs ||
		source.Rootfs.logicalID != request.VMID ||
		protectedCacheIdentity(cache.identity) != protectedCacheIdentity(source.Rootfs.cacheIdentity) {
		return VMRootfsExportResult{}, instanceStateError(request.VMID, "VM export source/cache binding differs")
	}
	backing, err := reopenProtectedBacking(ctx, source.Rootfs)
	if err != nil {
		return VMRootfsExportResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, backing.Release(context.WithoutCancel(ctx))) }()
	if record.lifecycle == instanceLifecycleCleaned {
		err = backing.RequireStoppedCapture(ctx)
	} else {
		err = backing.RequireExclusiveCapture(ctx, record.vmID)
	}
	if err != nil {
		return VMRootfsExportResult{}, err
	}
	if err := recoverVMRootfsExport(ctx, locked, journal, control, source); err != nil {
		return VMRootfsExportResult{}, err
	}
	originalState := "Stopped"
	if control != nil {
		originalState, err = control.state(ctx)
		if err != nil {
			return VMRootfsExportResult{}, err
		}
	}
	finalState = originalState
	progress := vmRootfsExportProgress{Schema: 1, OwnerUID: ownerUID, OwnerGID: ownerGID,
		VMID: request.VMID, ImageID: request.ImageID, CacheRoot: request.CacheRoot,
		Generation: record.launchGeneration, BootID: source.HostBootID, Rootfs: source.Rootfs,
		OriginalState: originalState, Stage: "prepared"}
	output, err := prepareVMRootfsExportOutput(ctx, locked.directories, progress)
	if err != nil {
		return VMRootfsExportResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, output.close(context.WithoutCancel(ctx))) }()
	var parent unix.Stat_t
	if err := unix.Fstat(output.entry.directory, &parent); err != nil {
		return VMRootfsExportResult{}, err
	}
	progress.OutputDevice, progress.OutputInode = uint64(output.identity.Dev), output.identity.Ino
	progress.ParentDevice, progress.ParentInode = uint64(parent.Dev), parent.Ino
	if err := journal.write(ctx, progress); err != nil {
		return VMRootfsExportResult{}, err
	}
	// Cleanup has its own bounded context. It cannot race unfinished copy I/O:
	// copy is synchronous, and this defer runs before control/backing/lifecycle release.
	defer func() {
		if returnErr == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), vmRootfsExportRecoveryTimeout)
		defer cancel()
		var restoreErr error
		finalState, restoreErr = restoreVMRootfsExportState(recovery, control, journal, &progress)
		removeErr := output.discard(recovery)
		returnErr = errors.Join(returnErr, restoreErr, removeErr)
		if restoreErr == nil && removeErr == nil {
			returnErr = errors.Join(returnErr, journal.remove(recovery))
		}
	}()
	if originalState == "Running" {
		progress.Stage = "pause_inflight"
		if err := journal.write(ctx, progress); err != nil {
			progress.Stage = "prepared" // Mutation was not sent.
			return VMRootfsExportResult{}, err
		}
		finalState = "unknown"
		if err := control.pause(ctx); err != nil {
			return VMRootfsExportResult{}, err
		}
		progress.Stage = "paused" // A complete authenticated pause response is settled evidence.
		state, err := control.state(ctx)
		if err != nil || state != "Paused" {
			return VMRootfsExportResult{}, instanceAtomicError("VM export pause was not confirmed", err)
		}
		finalState = "Paused"
		if err := journal.write(ctx, progress); err != nil {
			return VMRootfsExportResult{}, err
		}
	}
	progress.Stage = "copying"
	if err := journal.write(ctx, progress); err != nil {
		return VMRootfsExportResult{}, err
	}
	if err := copyVMRootfsExportBytes(ctx, backing.fd, output.fd, source.Rootfs.bytes); err != nil {
		return VMRootfsExportResult{}, err
	}
	progress.Stage = "copy_quiesced"
	if err := journal.write(ctx, progress); err != nil {
		return VMRootfsExportResult{}, err
	}
	finalState, err = finalizeVMRootfsExportState(ctx, control, journal, &progress)
	if err != nil {
		return VMRootfsExportResult{}, err
	}
	// Cancellation still restores the VM but never accepts a now-unwanted image copy.
	if err := ctx.Err(); err != nil {
		return VMRootfsExportResult{}, err
	}
	result, err := output.publish(ctx, source.Rootfs.bytes)
	if err != nil {
		return VMRootfsExportResult{}, err
	}
	progress.Stage = "complete"
	if err := journal.write(ctx, progress); err != nil {
		return VMRootfsExportResult{}, err
	}
	result.VMID, result.OriginalState, result.FinalState = request.VMID, originalState, finalState
	return result, nil
}
