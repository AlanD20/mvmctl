package jailer

import "context"

// prepareRuntimeDirectory creates and retains the per-VM runtime directory only after launching authority is durable.
// The caller identity and VM ID come from the retained record rather than from an untrusted effect request.
func (lease *launchLease) prepareRuntimeDirectory(
	ctx context.Context,
	deps managedVMRuntimeDeps,
	policy managedVMRuntimePolicy,
) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmLock == nil || state.directories == nil || state.prepared == nil ||
		state.processes == nil || state.runtimeDirectory != nil {
		return instanceStateError(state.record.vmID, "launch lease cannot prepare a managed VM runtime directory")
	}
	if ctx == nil {
		return instanceStateError(state.record.vmID, "managed VM runtime context is required")
	}
	if err := ctx.Err(); err != nil {
		return instanceAtomicError("prepare managed VM runtime directory", err)
	}
	if err := validateInstanceRecord(state.record); err != nil {
		return err
	}
	if state.record.lifecycle != instanceLifecycleLaunching || state.record.launch == nil {
		return instanceStateError(state.record.vmID, "launch authority is not awaiting runtime preparation")
	}

	caller := instanceCaller{uid: state.record.ownerUID, gid: state.record.launch.ownerGID}
	runtimeDirectory, err := createManagedVMRuntime(ctx, deps, policy, caller, state.record.vmID)
	if runtimeDirectory != nil {
		state.runtimeDirectory = runtimeDirectory
	}
	return err
}
