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
	privateRuntime, err := openPrivateVMRuntime(ctx, state.record, false)
	if err != nil {
		if privateRuntime != nil {
			err = appendInstanceOperationError(
				err,
				"close rejected private runtime",
				closeManagedVMRuntimeFDs(
					context.WithoutCancel(ctx),
					privateRuntime.state.deps,
					privateRuntime.state.retained,
				),
			)
		}
		if runtimeDirectory != nil {
			// Reopen is read-only; close its retained descriptors without removing live output.
			err = appendInstanceOperationError(
				err,
				"close operator runtime after private recovery error",
				closeManagedVMRuntimeFDs(
					context.WithoutCancel(ctx),
					runtimeDirectory.state.deps,
					runtimeDirectory.state.retained,
				),
			)
		}
		return err
	}
	state.runtimeDirectory = runtimeDirectory
	state.privateRuntimeDirectory = privateRuntime
	state.runtimeRecovery = nil
	return nil
}
