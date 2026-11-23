package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// RemoveFixedLeaves unlinks only the five compiled ephemeral output names through the retained VM directory.
// A missing leaf is already clean; directories, mounts, and every unexpected name remain untouched.
func (lease *managedVMRuntimeLease) RemoveFixedLeaves(ctx context.Context) error {
	if ctx == nil {
		return instanceAtomicError("remove managed VM runtime leaves: context is required", nil)
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
	if !state.leafCreated || state.vmDirectoryFD < 0 || state.identity == (managedVMRuntimeIdentity{}) {
		return instanceStateError(state.vmID, "managed VM runtime leaf cleanup authority is incomplete")
	}

	cleanupCtx := context.WithoutCancel(ctx)
	retainedIdentity, err := inspectManagedVMRuntimeIdentity(
		cleanupCtx,
		state.deps,
		state.vmDirectoryFD,
		"retained VM runtime before fixed-leaf cleanup",
	)
	if err != nil {
		return err
	}
	if retainedIdentity != state.identity {
		return instanceAtomicError("retained managed VM runtime directory identity changed", nil)
	}
	for _, leaf := range managedVMRuntimeFixedLeafNames() {
		if err := state.deps.unlinkAt(cleanupCtx, state.vmDirectoryFD, leaf, 0); err != nil &&
			!errors.Is(err, unix.ENOENT) {
			return instanceAtomicError("remove managed VM runtime leaf "+leaf, err)
		}
	}
	if err := state.deps.fsync(cleanupCtx, state.vmDirectoryFD); err != nil {
		return instanceAtomicError("sync managed VM runtime fixed-leaf cleanup", err)
	}
	return nil
}

func managedVMRuntimeFixedLeafNames() [5]string {
	return [5]string{
		infra.VMFirecrackerAPISocketFilename,
		infra.VMVsockSocketFilename,
		infra.VMConsoleSocketFilename,
		infra.VMFirecrackerPIDFilename,
		infra.VMConsolePIDFilename,
	}
}
