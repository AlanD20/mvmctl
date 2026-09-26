package jailer

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

type vmExecutionRetirementProof struct {
	state *vmExecutionRetirementProofState
}
type vmExecutionRetirementProofState struct {
	mu       sync.Mutex
	vmLock   *vmAuthorityLease
	ownerUID uint32
	vmID     string
	identity vmExecutionIdentity
	consumed bool
}

func retireRemovedVMExecution(ctx context.Context, locked *lockedInstance, caller instanceCaller) (returnErr error) {
	if locked == nil || locked.vmLock == nil || !locked.vmLock.isActive() ||
		locked.record.lifecycle != instanceLifecycleCleaned ||
		locked.record.ownerUID != caller.uid {
		return instanceStateError("", "execution retirement requires retained cleaned owner authority")
	}
	lease, err := locked.vmLock.openExecutionIdentity(ctx, caller, locked.record.vmID)
	if err != nil {
		return err
	}
	if lease == nil {
		return nil
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	identity, err := lease.Identity(ctx)
	if err != nil {
		return err
	}
	if err := requireExecutionRetirementTreesAbsent(ctx, caller.uid, locked.record.vmID); err != nil {
		return err
	}
	if err := requireProtectedVMGrantsAbsent(ctx, caller.uid, locked.record.vmID, identity.uid); err != nil {
		return err
	}
	if err := removeProtectedVMBackingArtifacts(ctx, caller.uid, locked.record.vmID); err != nil {
		return err
	}
	proof := &vmExecutionRetirementProof{state: &vmExecutionRetirementProofState{
		vmLock: locked.vmLock, ownerUID: caller.uid, vmID: locked.record.vmID, identity: identity,
	}}
	return locked.vmLock.retireExecutionIdentity(ctx, proof)
}

// No directory is created or adopted. Absence is relative to retained trusted
// parents, including the operator component of the private VMM runtime tree.
func requireExecutionRetirementTreesAbsent(ctx context.Context, ownerUID uint32, vmID string) error {
	parents := [][]string{
		{"var", "lib", "mvmctl", "jailer", "firecracker"},
		{"run", "mvmctl", "vmm-runtime", strconv.FormatUint(uint64(ownerUID), 10)},
		{"run", "mvmctl", "runtime", strconv.FormatUint(uint64(ownerUID), 10)},
	}
	for _, components := range parents {
		if err := requireExecutionRetirementTreeAbsent(ctx, components, vmID); err != nil {
			return err
		}
	}
	return nil
}

func requireExecutionRetirementTreeAbsent(ctx context.Context, components []string, vmID string) (returnErr error) {
	fd, err := unix.Open("/", instanceDirectoryFlags, 0)
	if err != nil {
		return instanceAtomicError("open execution retirement filesystem root", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	for _, component := range append([]string{""}, components...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if component != "" {
			next, err := unix.Openat2(
				fd,
				component,
				&unix.OpenHow{
					Flags:   instanceDirectoryFlags,
					Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
				},
			)
			if errors.Is(err, unix.ENOENT) {
				return nil
			}
			if err != nil {
				return instanceAtomicError("open execution retirement parent", err)
			}
			old := fd
			fd = next
			if err := unix.Close(old); err != nil {
				return err
			}
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0022 != 0 {
			return instanceStateError(vmID, "execution retirement parent is not root controlled")
		}
	}
	var stat unix.Stat_t
	err = unix.Fstatat(fd, vmID, &stat, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return instanceAtomicError("prove execution retirement tree absence", err)
	}
	return instanceStateError(vmID, "execution retirement found residual VM tree")
}
