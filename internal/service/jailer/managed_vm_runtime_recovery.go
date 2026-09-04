package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

type managedVMRuntimeRecovery struct {
	deps   managedVMRuntimeDeps
	policy managedVMRuntimePolicy
}

// reopenManagedVMRuntime pins an existing per-VM runtime directory without creating or repairing any component.
// A nil lease with no error means the root-controlled path is absent and therefore already clean.
func reopenManagedVMRuntime(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	policy managedVMRuntimePolicy,
	caller instanceCaller,
	vmID string,
) (_ *managedVMRuntimeLease, returnErr error) {
	if ctx == nil {
		return nil, instanceAtomicError("reopen managed VM runtime: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("reopen managed VM runtime", err)
	}
	if err := validateManagedVMRuntimeDeps(deps); err != nil {
		return nil, err
	}
	if err := validateManagedVMRuntimePolicy(policy); err != nil {
		return nil, err
	}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("managed VM runtime VM ID is invalid")
	}

	retained := make([]int, 0, 6)
	defer func() {
		if retained == nil {
			return
		}
		if closeErr := closeManagedVMRuntimeFDs(ctx, deps, retained); closeErr != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"close reopened managed VM runtime descriptors",
				closeErr,
			)
		}
	}()

	rootFD, err := deps.open(ctx, policy.rootPath, managedVMRuntimeDirectoryFlags, 0)
	if err != nil {
		return nil, instanceAtomicError("open filesystem root for managed VM runtime recovery", err)
	}
	retained = append(retained, rootFD)
	if _, err := inspectManagedVMRuntimeDirectory(
		ctx,
		deps,
		rootFD,
		"filesystem root",
		policy.trustedUID,
		policy.trustedGID,
		0,
		false,
	); err != nil {
		return nil, err
	}

	runFD, err := openExistingManagedVMRuntimeDirectory(ctx, deps, rootFD, "run", policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, runFD)
	mvmctlFD, present, err := openOptionalManagedVMRuntimeParent(ctx, deps, runFD, "mvmctl", policy)
	if err != nil || !present {
		return nil, err
	}
	retained = append(retained, mvmctlFD)
	runtimeFD, present, err := openOptionalManagedVMRuntimeParent(ctx, deps, mvmctlFD, "runtime", policy)
	if err != nil || !present {
		return nil, err
	}
	retained = append(retained, runtimeFD)
	uidFD, present, err := openOptionalManagedVMRuntimeParent(
		ctx,
		deps,
		runtimeFD,
		managedVMRuntimeUIDName(caller.uid),
		policy,
	)
	if err != nil || !present {
		return nil, err
	}
	retained = append(retained, uidFD)

	how := unix.OpenHow{
		Flags:   uint64(managedVMRuntimeDirectoryFlags),
		Resolve: uint64(managedVMRuntimeResolveManaged),
	}
	vmFD, err := deps.openAt2(ctx, uidFD, vmID, &how)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, instanceAtomicError("open existing managed VM runtime directory", err)
	}
	retained = append(retained, vmFD)
	identity, err := inspectManagedVMRuntimeDirectory(
		ctx,
		deps,
		vmFD,
		"reopened VM runtime",
		caller.uid,
		caller.gid,
		managedVMRuntimeVMMode,
		true,
	)
	if err != nil {
		return nil, err
	}
	lease := &managedVMRuntimeLease{state: &managedVMRuntimeLeaseState{
		deps:           deps,
		caller:         caller,
		vmID:           vmID,
		uidDirectoryFD: uidFD,
		vmDirectoryFD:  vmFD,
		identity:       identity,
		retained:       retained,
		leafCreated:    true,
	}}
	retained = nil
	return lease, nil
}

func openOptionalManagedVMRuntimeParent(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	parentFD int,
	name string,
	policy managedVMRuntimePolicy,
) (int, bool, error) {
	how := unix.OpenHow{Flags: uint64(managedVMRuntimeDirectoryFlags), Resolve: uint64(managedVMRuntimeResolveManaged)}
	fd, err := deps.openAt2(ctx, parentFD, name, &how)
	if errors.Is(err, unix.ENOENT) {
		return -1, false, nil
	}
	if err != nil {
		return -1, false, instanceAtomicError("open existing managed VM runtime parent "+name, err)
	}
	if _, err := inspectManagedVMRuntimeDirectory(
		ctx,
		deps,
		fd,
		name,
		policy.trustedUID,
		policy.trustedGID,
		managedVMRuntimeParentMode,
		true,
	); err != nil {
		return -1, false, closeRejectedManagedVMRuntimeFD(ctx, deps, fd, err)
	}
	return fd, true, nil
}

func managedVMRuntimePolicyFromInstanceAuthority(policy instanceAuthorityPolicy) managedVMRuntimePolicy {
	return managedVMRuntimePolicy{
		rootPath:   policy.rootPath,
		trustedUID: policy.expectedUID,
		trustedGID: policy.expectedGID,
	}
}
