package jailer

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"sort"
	"strconv"

	"golang.org/x/sys/unix"
)

// The lifecycle owner calls this before publishing launching. Thereafter a missing family is
// lost authority, never evidence that the VM has no grants.
func prepareProtectedVMGrantInventory(ctx context.Context, ownerUID uint32) (returnErr error) {
	owner, retained, err := openProtectedInventoryOwner(ctx, ownerUID, true)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(retained)) }()
	logical, err := openProtectedDirectory(ctx, owner, "logical", true, true)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(logical)) }()
	for _, kind := range protectedInventoryKinds() {
		fd, err := openProtectedDirectory(ctx, logical, kind, true, true)
		if err != nil {
			return err
		}
		if err := unix.Close(fd); err != nil {
			return managedVMResourceCleanupError("close protected inventory kind", err)
		}
	}
	return nil
}

func protectedInventoryKinds() []string {
	return []string{protectedBackingCloudInit, protectedBackingKernel, protectedBackingRootfs, protectedBackingVolume}
}

func openProtectedInventoryOwner(ctx context.Context, ownerUID uint32, create bool) (_ int, _ []int, returnErr error) {
	if ownerUID == 0 || ownerUID == math.MaxUint32 {
		return -1, nil, managedVMResourceValidationError("protected inventory owner is invalid")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, nil, managedVMResourceAtomicError("open protected inventory root", err)
	}
	retained := []int{fd}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	for index, component := range []string{"var", "lib", "mvmctl", "protected-backing", strconv.FormatUint(uint64(ownerUID), 10)} {
		fd, err = openProtectedDirectory(ctx, fd, component, create && index >= 3, index >= 3)
		if err != nil {
			return -1, nil, err
		}
		retained = append(retained, fd)
	}
	return fd, retained, nil
}

type protectedInventoryEntry struct {
	binding protectedBackingBinding
	record  protectedBackingRecord
}

// inventoryProtectedVMBackings reads fixed root metadata only. The lifecycle VM lock excludes
// new grants for this VM while stop/removal runs. Other VMs may update shared objects; every
// effect reacquires the object's lock and admits its current record before mutation.
func inventoryProtectedVMBackings(
	ctx context.Context,
	ownerUID uint32,
	vmID string,
	executionUID uint32,
) (_ []protectedInventoryEntry, returnErr error) {
	if !isCanonicalManagedResourceID(vmID, 32) {
		return nil, managedVMResourceValidationError("protected inventory VM identity is invalid")
	}
	owner, retained, err := openProtectedInventoryOwner(ctx, ownerUID, false)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(retained)) }()
	logical, err := openProtectedDirectory(ctx, owner, "logical", false, true)
	if err != nil {
		return nil, err
	}
	retained = append(retained, logical)
	var entries []protectedInventoryEntry
	for _, kind := range protectedInventoryKinds() {
		found, err := inventoryProtectedVMKind(ctx, owner, logical, ownerUID, kind, vmID, executionUID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, found...)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].binding.kind != entries[j].binding.kind {
			return entries[i].binding.kind < entries[j].binding.kind
		}
		return entries[i].binding.logicalID < entries[j].binding.logicalID
	})
	return entries, nil
}

func inventoryProtectedVMKind(
	ctx context.Context,
	owner, logical int,
	ownerUID uint32,
	kind, vmID string,
	executionUID uint32,
) (_ []protectedInventoryEntry, returnErr error) {
	fd, err := openProtectedDirectory(ctx, logical, kind, false, true)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "protected-logical-inventory")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var matches []protectedInventoryEntry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		names, readErr := file.Readdirnames(128)
		for _, id := range names {
			if !isCanonicalManagedResourceID(id, protectedKindIDLength(kind)) {
				return nil, managedVMResourceValidationError("protected logical inventory contains an unknown entry")
			}
			entry, initialized, err := readProtectedInventoryEntry(ctx, owner, fd, ownerUID, kind, id)
			if err != nil {
				return nil, err
			}
			if !initialized {
				continue
			}
			wanted := id == vmID && (kind == protectedBackingRootfs || kind == protectedBackingCloudInit)
			for _, references := range [][]protectedBackingReference{entry.record.References, entry.record.NextReferences} {
				for _, reference := range references {
					wanted = wanted || reference.VMID == vmID ||
						(executionUID != 0 && reference.ExecutionUID == executionUID)
				}
			}
			if wanted {
				matches = append(matches, entry)
			}
		}
		if readErr == io.EOF {
			return matches, nil
		}
		if readErr != nil {
			return nil, managedVMResourceAtomicError("enumerate protected logical inventory", readErr)
		}
	}
}

func readProtectedInventoryEntry(
	ctx context.Context,
	owner, kindFD int,
	ownerUID uint32,
	kind, id string,
) (_ protectedInventoryEntry, _ bool, returnErr error) {
	var result protectedInventoryEntry
	logical, err := openProtectedDirectory(ctx, kindFD, id, false, true)
	if err != nil {
		return result, false, err
	}
	retained := []int{logical}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(retained)) }()
	var selector protectedLogicalRecord
	if err := readProtectedRecord(ctx, logical, "logical.json", &selector); err != nil {
		return result, false, err
	}
	if selector.Version != 1 || selector.Owner != ownerUID || selector.Kind != kind || selector.ID != id ||
		selector.Cache.Inode == 0 {
		return result, false, managedVMResourceValidationError("protected logical inventory selector conflicts")
	}
	if !selector.Initialized {
		return result, false, nil
	}
	cache := managedCacheIdentity{
		deviceMajor: selector.Cache.Major,
		deviceMinor: selector.Cache.Minor,
		inode:       selector.Cache.Inode,
	}
	parent := owner
	for _, component := range []string{protectedCacheKey(cache), kind, id} {
		fd, err := openProtectedDirectory(ctx, parent, component, false, true)
		if err != nil {
			return result, false, err
		}
		retained = append(retained, fd)
		parent = fd
	}
	if err := readProtectedRecord(ctx, parent, "record.json", &result.record); err != nil {
		return result, false, err
	}
	record := result.record
	if record.Version != 1 || record.Owner != ownerUID || record.Kind != kind || record.ID != id ||
		record.Cache != selector.Cache {
		return result, false, managedVMResourceValidationError(
			"protected inventory record conflicts with logical identity",
		)
	}
	switch record.Phase {
	case "copying", "copied", "cancelling-copy", "cancelling-copied", "initial-removing", "removed", "removing":
		if (record.Phase == "cancelling-copy" || record.Phase == "cancelling-copied") &&
			kind != protectedBackingRootfs && kind != protectedBackingCloudInit {
			return result, false, managedVMResourceValidationError(
				"shared protected object has a VM import cancellation phase",
			)
		}
		if len(record.References) != 0 || len(record.NextReferences) != 0 {
			return result, false, managedVMResourceValidationError("inactive protected object retains grants")
		}
	case "ready", "growing", "acl":
		if record.Backing.Inode == 0 || record.Backing.Size <= 0 {
			return result, false, managedVMResourceValidationError("protected inventory backing identity is absent")
		}
	default:
		return result, false, managedVMResourceValidationError("protected inventory phase is unknown")
	}
	result.binding = protectedBackingBinding{
		kind: kind, logicalID: id, ownerUID: ownerUID, cacheIdentity: cache, bytes: uint64(record.Backing.Size),
		identity: managedVMConfigStat{
			deviceMajor: unix.Major(record.Backing.Device),
			deviceMinor: unix.Minor(record.Backing.Device),
			inode:       record.Backing.Inode,
			uid:         0,
			gid:         0,
			mode:        unix.S_IFREG | 0600,
			nlink:       1,
			size:        uint64(record.Backing.Size),
		},
	}
	return result, true, nil
}

func requireProtectedVMGrantsAbsent(ctx context.Context, ownerUID uint32, vmID string, executionUID uint32) error {
	entries, err := inventoryProtectedVMBackings(ctx, ownerUID, vmID, executionUID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		for _, references := range [][]protectedBackingReference{entry.record.References, entry.record.NextReferences} {
			for _, reference := range references {
				if reference.VMID == vmID || reference.ExecutionUID == executionUID {
					return managedVMResourceValidationError("VM execution identity still has protected backing grants")
				}
			}
		}
	}
	return nil
}

func revokeProtectedVMGrants(ctx context.Context, ownerUID uint32, vmID string, executionUID uint32) error {
	entries, err := inventoryProtectedVMBackings(ctx, ownerUID, vmID, executionUID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		found := false
		for _, references := range [][]protectedBackingReference{entry.record.References, entry.record.NextReferences} {
			for _, reference := range references {
				if reference.ExecutionUID == executionUID && reference.VMID != vmID {
					return managedVMResourceValidationError("execution identity is referenced by a different VM")
				}
				if reference.VMID == vmID {
					if reference.ExecutionUID != executionUID {
						return managedVMResourceValidationError("protected VM grant execution identity conflicts")
					}
					found = true
				}
			}
		}
		if !found {
			continue
		}
		lease, err := reopenProtectedBacking(ctx, entry.binding)
		if err != nil {
			return err
		}
		if err := errors.Join(lease.Revoke(ctx, vmID, executionUID), lease.Release(context.WithoutCancel(ctx))); err != nil {
			return err
		}
	}
	return requireProtectedVMGrantsAbsent(ctx, ownerUID, vmID, executionUID)
}

func removeProtectedVMBackingArtifacts(ctx context.Context, retirement *protectedImportRetirement) error {
	if err := retirement.requireAuthority(ctx); err != nil {
		return err
	}
	state := retirement.locked.vmLock.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := retirement.requireLockedAuthority(state); err != nil {
		return err
	}
	if err := retirement.requireAbsence(ctx); err != nil {
		return err
	}
	ownerUID, vmID := retirement.ownerUID, retirement.vmID
	entries, err := inventoryProtectedVMBackings(ctx, ownerUID, vmID, 0)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.binding.logicalID != vmID ||
			(entry.binding.kind != protectedBackingRootfs && entry.binding.kind != protectedBackingCloudInit) || entry.record.Phase == "removed" {
			continue
		}
		cache, err := reopenProtectedCache(ctx, ownerUID, entry.binding.cacheIdentity)
		if err != nil {
			return err
		}
		if entry.record.Phase == "copying" || entry.record.Phase == "copied" ||
			entry.record.Phase == "cancelling-copy" || entry.record.Phase == "cancelling-copied" {
			cancellation, openErr := openProtectedImportCancellation(ctx, cache, entry.binding.kind, vmID)
			if openErr != nil {
				return errors.Join(openErr, cache.Release(context.WithoutCancel(ctx)))
			}
			cancelErr := cancellation.cancel(ctx, productionProtectedImportCancellationDeps())
			closeErr := errors.Join(closeProtectedFDs(cancellation.retained), cache.Release(context.WithoutCancel(ctx)))
			if err := errors.Join(cancelErr, closeErr); err != nil {
				return err
			}
			continue
		}
		lease, found, lookupErr := lookupProtectedBacking(ctx, cache, entry.binding.kind, vmID)
		closeErr := cache.Release(context.WithoutCancel(ctx))
		if lookupErr != nil || closeErr != nil {
			if lease != nil {
				closeErr = errors.Join(closeErr, lease.Release(context.WithoutCancel(ctx)))
			}
			return errors.Join(lookupErr, closeErr)
		}
		if !found {
			return errors.Join(
				managedVMResourceValidationError("initialized VM backing disappeared"),
				lease.Release(context.WithoutCancel(ctx)),
			)
		}
		if err := errors.Join(lease.Remove(ctx), lease.Release(context.WithoutCancel(ctx))); err != nil {
			return err
		}
	}
	return nil
}
