package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// Remove removes only the retained empty VM directory after rechecking its exact entry identity.
func (lease *managedVMRuntimeLease) Remove(ctx context.Context) error {
	if ctx == nil {
		return instanceAtomicError("remove managed VM runtime: context is required", nil)
	}
	if lease == nil || lease.state == nil {
		return instanceStateError("", "managed VM runtime lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return instanceStateError(state.vmID, "managed VM runtime lease is released")
	}
	if state.removed {
		return nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if state.parentSyncPending {
		if err := state.deps.fsync(cleanupCtx, state.uidDirectoryFD); err != nil {
			return instanceAtomicError("sync removed managed VM runtime directory", err)
		}
		state.parentSyncPending = false
		state.removed = true
		return nil
	}
	if !state.leafCreated || state.uidDirectoryFD < 0 || state.vmDirectoryFD < 0 ||
		state.identity == (managedVMRuntimeIdentity{}) {
		return instanceStateError(state.vmID, "managed VM runtime cleanup authority is incomplete")
	}
	retainedIdentity, err := inspectManagedVMRuntimeIdentity(
		cleanupCtx,
		state.deps,
		state.vmDirectoryFD,
		"retained VM runtime",
	)
	if err != nil {
		return err
	}
	if retainedIdentity != state.identity {
		return instanceAtomicError("retained managed VM runtime directory identity changed", nil)
	}
	how := unix.OpenHow{
		Flags:   uint64(managedVMRuntimeDirectoryFlags),
		Resolve: uint64(managedVMRuntimeResolveManaged),
	}
	currentFD, err := state.deps.openAt2(cleanupCtx, state.uidDirectoryFD, state.vmID, &how)
	if err != nil {
		return instanceAtomicError("reopen managed VM runtime directory for cleanup", err)
	}
	currentIdentity, inspectErr := inspectManagedVMRuntimeIdentity(
		cleanupCtx,
		state.deps,
		currentFD,
		"reopened VM runtime",
	)
	closeErr := state.deps.close(cleanupCtx, currentFD)
	if inspectErr != nil {
		return appendInstanceOperationError(inspectErr, "close rejected reopened VM runtime directory", closeErr)
	}
	if closeErr != nil {
		return instanceAtomicError("close reopened managed VM runtime directory", closeErr)
	}
	if currentIdentity != state.identity {
		return instanceAtomicError("managed VM runtime directory entry identity changed", nil)
	}
	if err := state.deps.unlinkAt(cleanupCtx, state.uidDirectoryFD, state.vmID, unix.AT_REMOVEDIR); err != nil {
		primary := instanceAtomicError("remove managed VM runtime directory", err)
		if reconcileErr := reconcileManagedVMRuntimeRemoval(cleanupCtx, state); reconcileErr != nil {
			return appendInstanceOperationError(
				primary,
				"reconcile managed VM runtime directory removal",
				reconcileErr,
			)
		}
		return primary
	}
	state.leafCreated = false
	state.parentSyncPending = true
	if err := state.deps.fsync(cleanupCtx, state.uidDirectoryFD); err != nil {
		return instanceAtomicError("sync removed managed VM runtime directory", err)
	}
	state.parentSyncPending = false
	state.removed = true
	return nil
}

// Release closes the retained runtime descriptors exactly once after successful removal.
func (lease *managedVMRuntimeLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.removed || state.leafCreated || state.parentSyncPending {
		return instanceStateError(state.vmID, "managed VM runtime directory must be removed before release")
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	state.releaseErr = closeManagedVMRuntimeFDs(cleanupCtx, state.deps, state.retained)
	if state.releaseErr != nil {
		state.releaseErr = instanceAtomicError("release managed VM runtime descriptors", state.releaseErr)
	}
	state.caller = instanceCaller{}
	state.vmID = ""
	state.uidDirectoryFD = -1
	state.vmDirectoryFD = -1
	state.identity = managedVMRuntimeIdentity{}
	state.retained = nil
	state.released = true
	return state.releaseErr
}

func reconcileManagedVMRuntimeRemoval(ctx context.Context, state *managedVMRuntimeLeaseState) error {
	how := unix.OpenHow{
		Flags:   uint64(managedVMRuntimeDirectoryFlags),
		Resolve: uint64(managedVMRuntimeResolveManaged),
	}
	currentFD, err := state.deps.openAt2(ctx, state.uidDirectoryFD, state.vmID, &how)
	if errors.Is(err, unix.ENOENT) {
		state.leafCreated = false
		state.parentSyncPending = true
		return nil
	}
	if err != nil {
		return instanceAtomicError("reopen managed VM runtime directory after removal error", err)
	}
	currentIdentity, inspectErr := inspectManagedVMRuntimeIdentity(
		ctx,
		state.deps,
		currentFD,
		"reconciled VM runtime",
	)
	closeErr := state.deps.close(ctx, currentFD)
	if inspectErr != nil {
		return appendInstanceOperationError(inspectErr, "close reconciled VM runtime directory", closeErr)
	}
	if closeErr != nil {
		return instanceAtomicError("close reconciled managed VM runtime directory", closeErr)
	}
	if currentIdentity != state.identity {
		return instanceAtomicError("managed VM runtime directory identity changed after removal error", nil)
	}
	return nil
}
