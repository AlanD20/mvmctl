package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

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
	root, err := inspectManagedVMCgroupDirectory(
		ctx,
		deps,
		rootFD,
		"recovered cgroup-v2 root",
		managedVMCgroupRootMode,
	)
	if err != nil {
		return nil, false, err
	}
	var filesystem unix.Statfs_t
	if err := deps.statfs(ctx, rootFD, &filesystem); err != nil {
		return nil, false, instanceAtomicError("inspect recovered cgroup-v2 root filesystem", err)
	}
	if filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, false, instanceAtomicError("fixed cgroup root is not a cgroup-v2 mount", nil)
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
	parent, err := inspectManagedVMCgroupDirectory(
		ctx,
		deps,
		parentFD,
		"recovered managed cgroup parent",
		managedVMCgroupParentMode,
	)
	if err != nil {
		return nil, false, err
	}
	if parent.mount != root.mount || parent.deviceMajor != root.deviceMajor || parent.deviceMinor != root.deviceMinor {
		return nil, false, instanceAtomicError(
			"recovered managed cgroup parent is outside the pinned cgroup-v2 mount",
			nil,
		)
	}

	leafFD, err := deps.openAt2(ctx, parentFD, vmID, &how)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, instanceAtomicError("open existing managed VM cgroup for recovery", err)
	}
	retained = append(retained, leafFD)
	leaf, err := inspectManagedVMCgroupDirectory(
		ctx,
		deps,
		leafFD,
		"recovered managed VM cgroup",
		managedVMCgroupLeafMode,
	)
	if err != nil {
		return nil, false, err
	}
	if leaf.mount != parent.mount || leaf.deviceMajor != parent.deviceMajor || leaf.deviceMinor != parent.deviceMinor {
		return nil, false, instanceAtomicError("recovered managed VM cgroup is outside the pinned cgroup-v2 mount", nil)
	}

	lease := &managedVMCgroupLease{state: &managedVMCgroupLeaseState{
		deps: deps, vmID: vmID, rootFD: rootFD, parentFD: parentFD, leafFD: leafFD,
		parent: parent, leaf: leaf, retained: retained,
	}}
	retained = nil
	return lease, true, lease.VerifyMembership(ctx, locator, observation, verifier)
}
