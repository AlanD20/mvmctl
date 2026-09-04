package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// requireFreshRecoveryManagedVMCgroupAbsent proves the fixed VM leaf is stably absent without creating, repairing,
// adopting, or deleting cgroup state. Because no descriptor escapes this call, a second full chain observation binds
// the result to the fixed hierarchy instead of a detached object replaced during the first observation.
//
// CALLER MUST HOLD: the per-VM authority lock for vmID across this entire call. Future recovery composition enters
// through lockedInstance.requireFreshRecoveryManagedVMCgroupAbsent after lockOwnedInstance and before Release transfers
// or drops that authority; BeginCleanup is deliberately not wired by this isolated proof slice.
func requireFreshRecoveryManagedVMCgroupAbsent(
	ctx context.Context,
	deps managedVMCgroupReadDeps,
	vmID string,
) (returnErr error) {
	if ctx == nil {
		return instanceAtomicError("require fresh-recovery managed VM cgroup absence: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("require fresh-recovery managed VM cgroup absence", err)
	}
	if err := validateManagedVMCgroupReadDeps(deps); err != nil {
		return err
	}
	if !vmIDPattern.MatchString(vmID) {
		return instanceValidationError("fresh-recovery managed VM cgroup VM ID is invalid")
	}

	retained := make([]int, 0, 6)
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"close fresh-recovery managed VM cgroup absence descriptors",
			closeManagedVMCgroupReadFDs(context.WithoutCancel(ctx), deps, retained),
		)
	}()
	how := unix.OpenHow{
		Flags:   uint64(managedVMCgroupDirectoryFlags),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}

	rootFD, err := deps.openRoot(ctx)
	if err != nil {
		return instanceAtomicError("open fixed cgroup-v2 root for fresh-recovery absence", err)
	}
	retained = append(retained, rootFD)
	root, err := inspectManagedVMCgroupRoot(ctx, deps, rootFD, "fresh-recovery cgroup-v2 root")
	if err != nil {
		return err
	}

	parentFD, err := deps.openAt2(ctx, rootFD, infra.JailerCgroupParent, &how)
	parentPresent := err == nil
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return instanceAtomicError("open existing managed cgroup parent for fresh-recovery absence", err)
	}
	var parent managedVMCgroupIdentity
	if parentPresent {
		retained = append(retained, parentFD)
		parent, err = inspectManagedVMCgroupChild(
			ctx,
			deps,
			parentFD,
			"fresh-recovery managed cgroup parent",
			managedVMCgroupParentMode,
			root,
		)
		if err != nil {
			return err
		}
	}

	leafFD := -1
	leafPresent := false
	var leaf managedVMCgroupIdentity
	if parentPresent {
		leafFD, err = deps.openAt2(ctx, parentFD, vmID, &how)
		leafPresent = err == nil
		if err != nil && !errors.Is(err, unix.ENOENT) {
			return instanceAtomicError("open managed VM cgroup leaf for fresh-recovery absence", err)
		}
		if leafPresent {
			retained = append(retained, leafFD)
			leaf, err = inspectManagedVMCgroupChild(
				ctx,
				deps,
				leafFD,
				"fresh-recovery managed VM cgroup leaf",
				managedVMCgroupLeafMode,
				parent,
			)
			if err != nil {
				return err
			}
		}
	}

	recheckRootFD, err := deps.openRoot(ctx)
	if err != nil {
		return instanceAtomicError("reopen fixed cgroup-v2 root for fresh-recovery absence", err)
	}
	retained = append(retained, recheckRootFD)
	recheckRoot, err := inspectManagedVMCgroupRoot(
		ctx,
		deps,
		recheckRootFD,
		"rechecked fresh-recovery cgroup-v2 root",
	)
	if err != nil {
		return err
	}
	if recheckRoot != root {
		return instanceAtomicError("fixed cgroup-v2 root was replaced during fresh-recovery absence proof", nil)
	}

	recheckParentFD, err := deps.openAt2(ctx, recheckRootFD, infra.JailerCgroupParent, &how)
	if !parentPresent {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return instanceAtomicError("recheck absent managed cgroup parent for fresh recovery", err)
		}
		retained = append(retained, recheckParentFD)
		if _, err := inspectManagedVMCgroupChild(
			ctx,
			deps,
			recheckParentFD,
			"appeared fresh-recovery managed cgroup parent",
			managedVMCgroupParentMode,
			recheckRoot,
		); err != nil {
			return err
		}
		return instanceAtomicError("managed cgroup parent appeared during fresh-recovery absence proof", nil)
	}
	if errors.Is(err, unix.ENOENT) {
		return instanceAtomicError("managed cgroup parent disappeared during fresh-recovery absence proof", nil)
	}
	if err != nil {
		return instanceAtomicError("reopen managed cgroup parent for fresh-recovery absence", err)
	}
	retained = append(retained, recheckParentFD)
	recheckParent, err := inspectManagedVMCgroupChild(
		ctx,
		deps,
		recheckParentFD,
		"rechecked fresh-recovery managed cgroup parent",
		managedVMCgroupParentMode,
		recheckRoot,
	)
	if err != nil {
		return err
	}
	if recheckParent != parent {
		return instanceAtomicError("managed cgroup parent was replaced during fresh-recovery absence proof", nil)
	}

	recheckLeafFD, err := deps.openAt2(ctx, recheckParentFD, vmID, &how)
	if !leafPresent {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return instanceAtomicError("recheck absent managed VM cgroup leaf for fresh recovery", err)
		}
		retained = append(retained, recheckLeafFD)
		if _, err := inspectManagedVMCgroupChild(
			ctx,
			deps,
			recheckLeafFD,
			"appeared fresh-recovery managed VM cgroup leaf",
			managedVMCgroupLeafMode,
			recheckParent,
		); err != nil {
			return err
		}
		return instanceAtomicError("managed VM cgroup leaf appeared during fresh-recovery absence proof", nil)
	}
	if errors.Is(err, unix.ENOENT) {
		return instanceAtomicError("managed VM cgroup leaf disappeared during fresh-recovery absence proof", nil)
	}
	if err != nil {
		return instanceAtomicError("reopen managed VM cgroup leaf for fresh-recovery absence", err)
	}
	retained = append(retained, recheckLeafFD)
	recheckLeaf, err := inspectManagedVMCgroupChild(
		ctx,
		deps,
		recheckLeafFD,
		"rechecked fresh-recovery managed VM cgroup leaf",
		managedVMCgroupLeafMode,
		recheckParent,
	)
	if err != nil {
		return err
	}
	if recheckLeaf != leaf {
		return instanceAtomicError("managed VM cgroup leaf was replaced during fresh-recovery absence proof", nil)
	}
	return instanceStateError(vmID, "managed VM cgroup exists during fresh-recovery absence proof")
}

func (locked *lockedInstance) requireFreshRecoveryManagedVMCgroupAbsent(
	ctx context.Context,
	deps managedVMCgroupReadDeps,
) error {
	if locked == nil || locked.vmLock == nil || !locked.vmLock.isActive() {
		vmID := ""
		if locked != nil {
			vmID = locked.record.vmID
		}
		return instanceStateError(vmID, "locked instance cannot prove managed VM cgroup absence")
	}
	return requireFreshRecoveryManagedVMCgroupAbsent(ctx, deps, locked.record.vmID)
}

// recoverManagedVMCgroup reopens only the fixed existing cgroup-v2 hierarchy for fresh recovery. Missing managed
// state is already clean. Once the exact leaf is admitted, ownership transfers to the returned lease before process
// membership verification, so every subsequent proof failure retains exact retry and cleanup authority.
func recoverManagedVMCgroup(
	ctx context.Context,
	deps managedVMCgroupDeps,
	vmID string,
	locator instanceProcessLocator,
	observation processObservation,
	verifier retainedCgroupProcessVerifier,
) (_ *managedVMCgroupLease, _ bool, returnErr error) {
	if ctx == nil {
		return nil, false, instanceAtomicError("recover managed VM cgroup: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, instanceAtomicError("recover managed VM cgroup", err)
	}
	if err := validateManagedVMCgroupDeps(deps); err != nil {
		return nil, false, err
	}
	readDeps := managedVMCgroupReadDepsFrom(deps)
	if !vmIDPattern.MatchString(vmID) {
		return nil, false, instanceValidationError("managed VM cgroup VM ID is invalid")
	}
	if verifier == nil {
		return nil, false, instanceAtomicError("retained cgroup process verifier is required", nil)
	}

	retained := make([]int, 0, 3)
	defer func() {
		if retained == nil {
			return
		}
		returnErr = appendInstanceOperationError(
			returnErr,
			"close rejected recovered managed VM cgroup descriptors",
			closeManagedVMCgroupFDs(context.WithoutCancel(ctx), deps, retained),
		)
	}()

	rootFD, err := deps.openRoot(ctx)
	if err != nil {
		return nil, false, instanceAtomicError("open fixed cgroup-v2 root for recovery", err)
	}
	retained = append(retained, rootFD)
	root, err := inspectManagedVMCgroupRoot(
		ctx,
		readDeps,
		rootFD,
		"recovered cgroup-v2 root",
	)
	if err != nil {
		return nil, false, err
	}

	how := unix.OpenHow{
		Flags:   uint64(managedVMCgroupDirectoryFlags),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}
	parentFD, err := deps.openAt2(ctx, rootFD, infra.JailerCgroupParent, &how)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, instanceAtomicError("open existing managed cgroup parent for recovery", err)
	}
	retained = append(retained, parentFD)
	parent, err := inspectManagedVMCgroupChild(
		ctx,
		readDeps,
		parentFD,
		"recovered managed cgroup parent",
		managedVMCgroupParentMode,
		root,
	)
	if err != nil {
		return nil, false, err
	}

	leafFD, err := deps.openAt2(ctx, parentFD, vmID, &how)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, instanceAtomicError("open existing managed VM cgroup for recovery", err)
	}
	retained = append(retained, leafFD)
	leaf, err := inspectManagedVMCgroupChild(
		ctx,
		readDeps,
		leafFD,
		"recovered managed VM cgroup",
		managedVMCgroupLeafMode,
		parent,
	)
	if err != nil {
		return nil, false, err
	}

	lease := &managedVMCgroupLease{state: &managedVMCgroupLeaseState{
		deps: deps, vmID: vmID, rootFD: rootFD, parentFD: parentFD, leafFD: leafFD,
		parent: parent, leaf: leaf, retained: retained,
	}}
	retained = nil
	return lease, true, lease.VerifyMembership(ctx, locator, observation, verifier)
}
