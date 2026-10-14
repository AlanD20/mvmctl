package jailer

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The lifecycle owner calls this before publishing launching. Thereafter a missing family is
// lost authority, never evidence that the VM has no grants.
func prepareProtectedVMGrantInventory(
	ctx context.Context,
	policy instanceAuthorityPolicy,
	ownerUID uint32,
) (returnErr error) {
	owner, retained, err := openProtectedInventoryOwner(ctx, policy, ownerUID, true)
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

func openProtectedInventoryOwner(
	ctx context.Context,
	policy instanceAuthorityPolicy,
	ownerUID uint32,
	create bool,
) (_ int, _ []int, returnErr error) {
	if ownerUID == 0 || ownerUID == math.MaxUint32 {
		return -1, nil, managedVMResourceValidationError("protected inventory owner is invalid")
	}
	fd, err := unix.Open(policy.rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
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
	policy instanceAuthorityPolicy,
	ownerUID uint32,
	vmID string,
	executionUID uint32,
) (_ []protectedInventoryEntry, returnErr error) {
	if !isCanonicalManagedResourceID(vmID, 32) {
		return nil, managedVMResourceValidationError("protected inventory VM identity is invalid")
	}
	owner, retained, err := openProtectedInventoryOwner(ctx, policy, ownerUID, false)
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
	if err := validateProtectedLogical(selector, ownerUID, kind, id); err != nil {
		return result, false, err
	}
	if !selector.Initialized {
		return result, false, nil
	}
	selectedCache := selector.Cache
	if kind == protectedBackingKernel {
		if selector.Kernel.Current == nil {
			return result, false, nil // Pending/aborted initial candidates cannot grant VM access.
		}
		selectedCache = selector.Kernel.Current.Cache
	}
	cache := managedCacheIdentity{
		deviceMajor: selectedCache.Major,
		deviceMinor: selectedCache.Minor,
		inode:       selectedCache.Inode,
	}
	parent := owner
	cacheKey := protectedCacheKey(cache)
	var selectedStore protectedBackingIdentity
	for _, component := range []string{cacheKey, kind, id} {
		fd, err := openProtectedDirectory(ctx, parent, component, false, true)
		if err != nil {
			return result, false, err
		}
		retained = append(retained, fd)
		parent = fd
		if component == cacheKey {
			var anchor protectedBackingAnchor
			if err := readProtectedRecord(ctx, fd, "store.json", &anchor); err != nil {
				return result, false, err
			}
			if err := validateProtectedStoreLayout(ctx, fd, anchor); err != nil {
				return result, false, err
			}
			if anchor.Owner != ownerUID || anchor.Cache != selectedCache {
				return result, false, managedVMResourceValidationError("protected inventory store authority conflicts")
			}
			selectedStore = anchor.Store
		}
	}
	if err := readProtectedRecord(ctx, parent, "record.json", &result.record); err != nil {
		return result, false, err
	}
	record := result.record
	if err := validateInitialVolumeRemoval(record); err != nil {
		return result, false, err
	}
	if record.Version != 1 || record.Owner != ownerUID || record.Kind != kind || record.ID != id ||
		record.Cache != selectedCache {
		return result, false, managedVMResourceValidationError(
			"protected inventory record conflicts with logical identity",
		)
	}
	if kind == protectedBackingKernel && selector.Kernel.Pending != nil &&
		selector.Kernel.Pending.Cache == selectedCache && selector.Kernel.Current.Phase == "removed" &&
		record.Generation == selector.Kernel.Pending.Generation {
		if len(record.References) != 0 || len(record.NextReferences) != 0 {
			return result, false, managedVMResourceValidationError("pending kernel import retains unexpected VM grants")
		}
		return result, false, nil
	}
	if kind == protectedBackingKernel && record.Phase == "kernel-aborted" {
		if err := requireProtectedKernelAbortedProjection(ctx, logical, selector, record, selectedStore); err != nil {
			return result, false, err
		}
		return result, false, nil // Exact immutable aborted receipt, never a live VM grant.
	}
	if kind == protectedBackingKernel && (record.Generation != selector.Kernel.Current.Generation ||
		record.Backing != selector.Kernel.Current.Backing) {
		return result, false, managedVMResourceValidationError("kernel inventory generation differs from logical authority")
	}
	switch record.Phase {
	case "copying", "copied", "cancelling-copy", "cancelling-copied", "initial-removing", "removed", "removing",
		"kernel-unprotected-removed", "kernel-reimport-copying", "kernel-reimport-copied":
		if strings.HasPrefix(record.Phase, "kernel-") && kind != protectedBackingKernel {
			return result, false, managedVMResourceValidationError("non-kernel backing has a kernel recovery phase")
		}
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
		generation: record.Generation,
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

func requireProtectedVMGrantsAbsent(
	ctx context.Context,
	policy instanceAuthorityPolicy,
	ownerUID uint32,
	vmID string,
	executionUID uint32,
) error {
	entries, err := inventoryProtectedVMBackings(ctx, policy, ownerUID, vmID, executionUID)
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

func revokeProtectedVMGrants(
	ctx context.Context,
	policy instanceAuthorityPolicy,
	ownerUID uint32,
	vmID string,
	executionUID uint32,
) error {
	entries, err := inventoryProtectedVMBackings(ctx, policy, ownerUID, vmID, executionUID)
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
		if err := errors.Join(
			lease.Revoke(ctx, vmID, executionUID),
			lease.Release(context.WithoutCancel(ctx)),
		); err != nil {
			return err
		}
	}
	return requireProtectedVMGrantsAbsent(ctx, policy, ownerUID, vmID, executionUID)
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
	entries, err := inventoryProtectedVMBackings(ctx, state.policy, ownerUID, vmID, 0)
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
