package jailer

import "context"

func resolveCleanupManagedVMRuntimeLocked(ctx context.Context, state *cleanupLeaseState) error {
	if state.runtimeRecovery == nil {
		return nil
	}
	if state.runtimeDirectory != nil || state.record.launch == nil {
		return instanceStateError(state.record.vmID, "managed VM runtime recovery authority is inconsistent")
	}
	caller := instanceCaller{uid: state.record.ownerUID, gid: state.record.launch.ownerGID}
	runtimeDirectory, err := reopenManagedVMRuntime(
		ctx,
		state.runtimeRecovery.deps,
		state.runtimeRecovery.policy,
		caller,
		state.record.vmID,
	)
	if err != nil {
		return err
	}
	state.runtimeDirectory = runtimeDirectory
	state.runtimeRecovery = nil
	return nil
}
