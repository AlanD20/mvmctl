package jailer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"sort"

	"golang.org/x/sys/unix"
)

const protectedBackingACL = "system.posix_acl_access"

func protectedACLBytes(references []protectedBackingReference) ([]byte, error) {
	permissions := map[uint32]uint16{0: 0}
	seen := make(map[string]bool, len(references))
	for _, reference := range references {
		if !isCanonicalManagedResourceID(reference.VMID, 32) || reference.ExecutionUID == 0 ||
			reference.ExecutionUID == math.MaxUint32 || seen[reference.VMID] {
			return nil, managedVMResourceValidationError("protected backing reference is invalid or duplicated")
		}
		seen[reference.VMID] = true
		permission := uint16(4)
		if reference.Writable {
			permission = 6
		}
		permissions[reference.ExecutionUID] |= permission
	}
	uids := make([]uint32, 0, len(permissions))
	for uid := range permissions {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	data := make([]byte, 4, 4+(len(uids)+4)*8)
	binary.LittleEndian.PutUint32(data, 2)
	entry := func(tag, permission uint16, id uint32) {
		data = binary.LittleEndian.AppendUint16(data, tag)
		data = binary.LittleEndian.AppendUint16(data, permission)
		data = binary.LittleEndian.AppendUint32(data, id)
	}
	entry(1, 6, math.MaxUint32)
	mask := uint16(0)
	for _, uid := range uids {
		entry(2, permissions[uid], uid)
		mask |= permissions[uid]
	}
	entry(4, 0, math.MaxUint32)
	entry(16, mask, math.MaxUint32)
	entry(32, 0, math.MaxUint32)
	return data, nil
}

func verifyProtectedACL(ctx context.Context, fd int, references []protectedBackingReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	expected, err := protectedACLBytes(references)
	if err != nil {
		return err
	}
	buffer := make([]byte, len(expected)+1)
	n, err := unix.Fgetxattr(fd, protectedBackingACL, buffer)
	if err != nil || n != len(expected) || !bytes.Equal(buffer[:n], expected) {
		return managedVMResourceAtomicError("protected backing ACL differs from root authority", err)
	}
	return nil
}

func setProtectedACL(ctx context.Context, fd int, references []protectedBackingReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := protectedACLBytes(references)
	if err != nil {
		return err
	}
	// The harmless named-root entry prevents the kernel from reducing an empty grant set to
	// plain mode bits. Successful exact readback therefore proves actual POSIX ACL support.
	if err := unix.Fsetxattr(fd, protectedBackingACL, data, 0); err != nil {
		return managedVMResourceAtomicError("apply exact protected backing ACL; filesystem support is required", err)
	}
	if err := verifyProtectedACL(ctx, fd, references); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return managedVMResourceAtomicError("sync protected backing ACL", err)
	}
	return nil
}

func (lease *protectedBackingLease) Grant(
	ctx context.Context, vmID string, executionUID uint32, access vmVolumeAccess,
) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.active(ctx); err != nil {
		return err
	}
	if !isCanonicalManagedResourceID(vmID, 32) || executionUID == 0 || executionUID == math.MaxUint32 ||
		executionUID == lease.ownerUID || (access != vmVolumeReadOnly && access != vmVolumeReadWrite) ||
		(lease.record.Kind == protectedBackingRootfs && lease.record.ID != vmID) {
		return managedVMResourceValidationError("protected backing grant is not an isolated VM identity")
	}
	if (lease.record.Kind == protectedBackingKernel || lease.record.Kind == protectedBackingCloudInit) &&
		access != vmVolumeReadOnly {
		return managedVMResourceValidationError("protected boot artifacts permit read-only VM access")
	}
	references := slices.Clone(lease.record.References)
	for _, reference := range references {
		if reference.VMID == vmID {
			if reference.ExecutionUID != executionUID || reference.Writable != (access == vmVolumeReadWrite) {
				return managedVMResourceValidationError("existing protected backing reference has different intent")
			}
			return verifyProtectedACL(ctx, lease.fd, references)
		}
		if reference.ExecutionUID == executionUID {
			return managedVMResourceValidationError("isolated execution identity is shared by different VMs")
		}
	}
	references = append(references, protectedBackingReference{
		VMID: vmID, ExecutionUID: executionUID, Writable: access == vmVolumeReadWrite,
	})
	sort.Slice(references, func(i, j int) bool { return references[i].VMID < references[j].VMID })
	return lease.changeACL(ctx, references)
}

// Revoke must run only after root proves the exact VM mount or task can no longer write. ACL
// removal does not revoke already-open descriptors in a live Firecracker process.
func (lease *protectedBackingLease) Revoke(ctx context.Context, vmID string, executionUID uint32) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.active(ctx); err != nil {
		return err
	}
	references := make([]protectedBackingReference, 0, len(lease.record.References))
	for _, reference := range lease.record.References {
		if reference.VMID != vmID {
			references = append(references, reference)
			continue
		}
		if reference.ExecutionUID != executionUID {
			return managedVMResourceValidationError("protected backing revocation execution identity differs")
		}
	}
	if len(references) == len(lease.record.References) {
		return verifyProtectedACL(ctx, lease.fd, references)
	}
	return lease.changeACL(ctx, references)
}

func (lease *protectedBackingLease) changeACL(ctx context.Context, references []protectedBackingReference) error {
	lease.record.NextReferences = references
	lease.record.Phase = "acl"
	if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
		return err
	}
	return lease.finishACL(ctx)
}

func (lease *protectedBackingLease) finishACL(ctx context.Context) error {
	identity, err := protectedIdentity(lease.fd)
	if err != nil || identity != lease.record.Backing {
		return managedVMResourceAtomicError("protected backing identity changed before ACL recovery", err)
	}
	if err := setProtectedACL(ctx, lease.fd, lease.record.NextReferences); err != nil {
		return err
	}
	lease.record.References = lease.record.NextReferences
	lease.record.NextReferences = nil
	lease.record.Phase = "ready"
	if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
		return err
	}
	lease.identity, err = stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), lease.fd)
	return err
}

// Grow changes the protected inode only while no VM retains access. Filesystem-level growth is
// a separate named consumer operation; this method establishes the backing-file transaction.
func (lease *protectedBackingLease) Grow(ctx context.Context, targetBytes uint64) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.record.Kind != protectedBackingRootfs && lease.record.Kind != protectedBackingVolume {
		return managedVMResourceValidationError("immutable launch backing cannot grow")
	}
	if err := lease.active(ctx); err != nil {
		return err
	}
	if len(lease.record.References) != 0 || targetBytes < uint64(lease.record.Backing.Size) ||
		targetBytes > math.MaxInt64 || (lease.record.Kind == protectedBackingRootfs && targetBytes > managedVMRootfsMaxBytes) {
		return managedVMResourceValidationError("protected backing growth requires unreferenced valid grow-only intent")
	}
	lease.record.Phase = "growing"
	lease.record.GrowBytes = targetBytes
	if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
		return err
	}
	return lease.finishGrow(ctx)
}

func (lease *protectedBackingLease) finishGrow(ctx context.Context) error {
	if len(lease.record.References) != 0 || lease.record.GrowBytes < uint64(lease.record.Backing.Size) ||
		lease.record.GrowBytes > math.MaxInt64 {
		return managedVMResourceValidationError("protected backing grow record is invalid")
	}
	if err := unix.Ftruncate(lease.fd, int64(lease.record.GrowBytes)); err != nil {
		return managedVMResourceAtomicError("grow protected backing inode", err)
	}
	if err := unix.Fsync(lease.fd); err != nil {
		return managedVMResourceAtomicError("sync grown protected backing", err)
	}
	lease.record.Backing.Size = int64(lease.record.GrowBytes)
	lease.record.GrowBytes = 0
	lease.record.Phase = "ready"
	if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
		return err
	}
	var err error
	lease.identity, err = stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), lease.fd)
	return err
}

func (lease *protectedBackingLease) Remove(ctx context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.released && lease.lockFD >= 0 && lease.record.Phase == "removed" {
		return ctx.Err()
	}
	if err := lease.active(ctx); err != nil {
		return err
	}
	if len(lease.record.References) != 0 || len(lease.record.NextReferences) != 0 {
		return managedVMResourceValidationError("referenced protected backing cannot be removed")
	}
	if lease.record.Kind == protectedBackingKernel {
		digest, err := hashProtectedKernel(ctx, lease.fd, lease.record.Backing)
		if err != nil {
			return err
		}
		lease.record.KernelDigest = digest
	}
	lease.record.Phase = "removing"
	if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
		return err
	}
	return lease.finishRemoval(ctx)
}

func (lease *protectedBackingLease) finishRemoval(ctx context.Context) error {
	if lease.record.Kind == protectedBackingKernel && !isCanonicalManagedResourceID(lease.record.KernelDigest, 64) {
		return managedVMResourceValidationError("protected kernel retirement has no retained root digest")
	}
	if len(lease.record.References) != 0 {
		return managedVMResourceValidationError("protected removal record retains VM references")
	}
	var stat unix.Stat_t
	err := unix.Fstatat(lease.directoryFD, "disk", &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		identity := protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size}
		if identity != lease.record.Backing || stat.Uid != 0 || stat.Nlink != 1 ||
			stat.Mode&unix.S_IFMT != unix.S_IFREG {
			return managedVMResourceValidationError("protected removal target identity changed")
		}
		if lease.record.Kind == protectedBackingKernel {
			fd, err := openProtectedFile(ctx, lease.directoryFD, "disk", unix.O_RDONLY)
			if err != nil {
				return err
			}
			digest, hashErr := hashProtectedKernel(ctx, fd, lease.record.Backing)
			var current unix.Stat_t
			statErr := unix.Fstatat(lease.directoryFD, "disk", &current, unix.AT_SYMLINK_NOFOLLOW)
			closeErr := unix.Close(fd)
			if err := errors.Join(hashErr, statErr, closeErr); err != nil {
				return err
			}
			if digest != lease.record.KernelDigest || !sameProtectedReadStat(stat, current) {
				return managedVMResourceValidationError("protected kernel changed after retirement intent")
			}
		}
		if err := unix.Unlinkat(lease.directoryFD, "disk", 0); err != nil {
			return managedVMResourceAtomicError("remove protected backing inode", err)
		}
	} else if err != unix.ENOENT {
		return managedVMResourceAtomicError("inspect protected removal target", err)
	}
	if err := unix.Fsync(lease.directoryFD); err != nil {
		return managedVMResourceAtomicError("sync protected backing removal", err)
	}
	lease.record.Phase = "removed"
	return writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record)
}
