package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// CALLER MUST HOLD: the global logical and destination object locks. Only unpublished, recorded
// candidates may be cancelled. Any visible disk publication retains pending recovery authority.
func (lease *protectedBackingLease) abortKernelImport(ctx context.Context) error {
	if lease.kernelGeneration == 0 {
		return nil
	}
	logical, err := readProtectedKernelLogical(ctx, lease.logicalFD, lease.ownerUID, lease.record.ID)
	if err != nil {
		return err
	}
	pending := logical.Kernel.Pending
	if pending == nil {
		return nil
	}
	if pending.Generation != lease.kernelGeneration || pending.Cache != lease.record.Cache ||
		!sameProtectedNode(pending.Directory, lease.record.Directory) {
		return managedVMResourceValidationError("kernel abort differs from recorded pending generation")
	}
	var disk unix.Stat_t
	err = unix.Fstatat(lease.directoryFD, "disk", &disk, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return nil // Publication may already have applied; only explicit import can reconcile it.
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if pending.Phase != "aborting" {
		var named unix.Stat_t
		inspectErr := unix.Fstatat(lease.directoryFD, "pending", &named, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(inspectErr, unix.ENOENT) && lease.record.Backing.Inode != 0 {
			return managedVMResourceValidationError("recorded kernel candidate is missing; absence is not abort authority")
		}
		if inspectErr != nil && !errors.Is(inspectErr, unix.ENOENT) {
			return inspectErr
		}
		pending.Candidate = lease.record.Backing
		pending.Phase = "aborting"
		if err := writeProtectedRecord(ctx, lease.logicalFD, "logical.json", logical); err != nil {
			return err
		}
	} else if err := syncProtectedCacheDirectory(ctx, lease.logicalFD); err != nil {
		return err
	}
	var candidate unix.Stat_t
	err = unix.Fstatat(lease.directoryFD, "pending", &candidate, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		identity := protectedBackingIdentity{Device: uint64(candidate.Dev), Inode: candidate.Ino, Size: candidate.Size}
		if !sameProtectedNode(identity, pending.Candidate) || candidate.Uid != 0 || candidate.Gid != 0 ||
			candidate.Nlink != 1 || candidate.Mode&unix.S_IFMT != unix.S_IFREG ||
			candidate.Mode&07007 != 0 || candidate.Size < 0 || uint64(candidate.Size) > managedVMKernelMaxBytes {
			return managedVMResourceValidationError("kernel abort candidate differs from root authority")
		}
		if err := unix.Unlinkat(lease.directoryFD, "pending", 0); err != nil {
			return err
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Fsync(lease.directoryFD); err != nil {
		return err
	}
	lease.record.Phase = "kernel-aborted"
	if pending.SnapshotSource != nil && lease.record.Source.Inode == 0 {
		// Preserve pre-copy provenance so only this initial trusted producer can retry its abort.
		lease.record.Source = *pending.SnapshotSource
	}
	if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
		return err
	}
	receipt := protectedKernelReceipt{Version: protectedKernelLogicalVersion, Owner: lease.ownerUID,
		ID: lease.record.ID, Binding: protectedKernelIncarnation{Generation: pending.Generation,
			Cache: pending.Cache, Store: pending.Store, Backing: pending.Candidate, Phase: "aborted"}}
	if err := retainProtectedKernelReceipt(ctx, lease.logicalFD, receipt); err != nil {
		return err
	}
	logical.Kernel.Pending = nil
	return writeProtectedRecord(ctx, lease.logicalFD, "logical.json", logical)
}
