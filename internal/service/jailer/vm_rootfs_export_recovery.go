package jailer

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const vmRootfsExportRecoveryTimeout = 30 * time.Second

// This private contract is the three observed control operations needed by disk
// export. The authenticated snapshot session implements it without any wire passthrough.
type vmRootfsExportControl interface {
	state(context.Context) (string, error)
	pause(context.Context) error
	resume(context.Context) error
}

func vmRootfsExportError(vmID, state string, cause error) error {
	return errs.WrapMsg(errs.CodeImageImportFailed,
		"VM rootfs export did not complete; source state is "+state, cause,
		errs.WithDetails(map[string]any{"source_vm_id": vmID, "vm_state": state}))
}

// restoreVMRootfsExportState runs after synchronous copy I/O has ceased, retaining
// the lifecycle/backing/control leases. It never retries a possibly applied mutation.
func restoreVMRootfsExportState(
	ctx context.Context,
	control vmRootfsExportControl,
	journal *vmRootfsExportJournal,
	progress *vmRootfsExportProgress,
) (string, error) {
	if progress.OriginalState == "Stopped" {
		progress.Stage = "restored"
		return "Stopped", journal.write(ctx, *progress)
	}
	if progress.Stage == "pause_inflight" || progress.Stage == "resume_inflight" {
		return "unknown", instanceStateError(progress.VMID,
			"VM export control outcome is uncertain; stop the source before retrying import")
	}
	current, err := control.state(ctx)
	if err != nil {
		return "unknown", err
	}
	if progress.OriginalState == "Paused" {
		if current != "Paused" {
			return "unknown", instanceStateError(progress.VMID, "originally paused export source changed state")
		}
	} else if current == "Paused" {
		// prepared has no durable evidence that this transaction applied pause.
		if progress.Stage == "prepared" {
			return "unknown", instanceStateError(progress.VMID, "unpaused export source changed state")
		}
		progress.Stage = "resume_inflight"
		if err := journal.write(ctx, *progress); err != nil {
			return "unknown", err
		}
		if err := control.resume(ctx); err != nil {
			return "unknown", err
		}
		current, err = control.state(ctx)
		if err != nil || current != "Running" {
			return "unknown", instanceAtomicError("VM export resume was not confirmed", err)
		}
	}
	if current != progress.OriginalState {
		return "unknown", instanceStateError(progress.VMID, "VM export source state could not be restored")
	}
	progress.Stage = "restored"
	return current, journal.write(ctx, *progress)
}

func finalizeVMRootfsExportState(
	ctx context.Context,
	control vmRootfsExportControl,
	journal *vmRootfsExportJournal,
	progress *vmRootfsExportProgress,
) (string, error) {
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), vmRootfsExportRecoveryTimeout)
	defer cancel()
	return restoreVMRootfsExportState(recovery, control, journal, progress)
}

// Recovery is entered only after acquiring the same never-unlinked VM lock; the
// former receiver has relinquished its copy FD and cannot continue reading bytes.
func recoverVMRootfsExport(
	ctx context.Context, locked *lockedInstance, journal *vmRootfsExportJournal,
	control vmRootfsExportControl, current vmLaunchDeviceAuthority,
) (returnErr error) {
	progress, err := journal.read(ctx)
	if err != nil || progress == nil {
		return err
	}
	record := locked.record
	if progress.OwnerUID != record.ownerUID {
		return instanceStateError(record.vmID, "VM export recovery owner differs")
	}
	if progress.Stage != "complete" &&
		(progress.Generation != record.launchGeneration ||
			progress.BootID != current.HostBootID || progress.Rootfs != current.Rootfs) {
		return vmRootfsExportError(record.vmID, "unknown",
			instanceStateError(record.vmID, "VM export recovery source generation or backing changed"))
	}
	if progress.Stage != "complete" {
		if record.lifecycle == instanceLifecycleCleaned {
			// Exact lifecycle cleanup proves the old process exited. Never resume it.
			progress.Stage = "restored"
			if err := journal.write(ctx, *progress); err != nil {
				return err
			}
		} else if _, err := restoreVMRootfsExportState(ctx, control, journal, progress); err != nil {
			return vmRootfsExportError(record.vmID, "unknown", err)
		}
		if err := removeRecoveredVMRootfsExportOutput(ctx, locked, *progress); err != nil {
			return err
		}
	}
	return journal.remove(ctx)
}

func removeRecoveredVMRootfsExportOutput(
	ctx context.Context, locked *lockedInstance, progress vmRootfsExportProgress,
) (returnErr error) {
	if progress.OutputInode == 0 {
		return nil
	}
	entry, err := lockVMRootfsExportStoreEntry(ctx, locked.directories, progress.ImageID)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, entry.close(context.WithoutCancel(ctx))) }()
	if entry.record == nil || entry.record.Source.VMID != progress.VMID ||
		entry.record.Source.Generation != progress.Generation ||
		entry.record.Source.BootID != progress.BootID ||
		entry.record.Source.Rootfs != progress.Rootfs ||
		entry.record.File != (vmRootfsExportNode{Device: progress.OutputDevice, Inode: progress.OutputInode}) ||
		entry.record.Directory != (vmRootfsExportNode{Device: progress.ParentDevice, Inode: progress.ParentInode}) {
		return instanceStateError(progress.VMID, "VM export recovery copy binding differs")
	}
	return entry.releaseData(ctx)
}

// Called only after cleanup durably records cleaned under the retained VM lock.
// A pending export cannot keep a process alive or resume it during termination.
func retireStoppedVMRootfsExport(
	ctx context.Context, dirs *instanceUIDDirectories, record instanceRecord,
) (returnErr error) {
	journal, err := openVMRootfsExportJournal(ctx, dirs, record.vmID, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, journal.close(context.WithoutCancel(ctx))) }()
	progress, err := journal.read(ctx)
	if err != nil || progress == nil || progress.Stage == "complete" {
		return err
	}
	if record.lifecycle != instanceLifecycleCleaned || progress.OwnerUID != record.ownerUID ||
		progress.Generation != record.launchGeneration {
		return instanceStateError(record.vmID, "VM export termination binding differs")
	}
	if err := removeRecoveredVMRootfsExportOutput(
		ctx,
		&lockedInstance{record: record, directories: dirs},
		*progress,
	); err != nil {
		return err
	}
	return journal.remove(ctx)
}
