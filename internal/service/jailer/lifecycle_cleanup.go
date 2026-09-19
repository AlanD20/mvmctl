package jailer

import "context"

// removeLifecycleJailTree first consumes the existing pidfd termination/reap authority, then
// removes only the fixed stopped tree. Complete still owns cgroup/runtime cleanup and cleaned.
func (lease *cleanupLease) removeLifecycleJailTree(ctx context.Context) error {
	if ctx == nil || lease == nil || lease.state == nil {
		return instanceStateError("", "lifecycle cleanup authority and context are required")
	}
	if err := lease.admitFreshRecovery(ctx); err != nil {
		return err
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmLock == nil || !state.vmLock.isActive() || state.directories == nil ||
		state.record.lifecycle != instanceLifecycleCleaning || state.freshRecoveryAdmission != nil || state.freshRecoveryAdmitting {
		return instanceStateError(state.record.vmID, "lifecycle cleanup authority is not ready")
	}
	if state.processes != nil {
		if err := state.processes.terminateAndReap(ctx); err != nil {
			return err
		}
	}
	if state.recoveredProcesses != nil {
		if err := state.recoveredProcesses.terminateAndReap(ctx); err != nil {
			return err
		}
	}
	if err := removeStoppedJailTree(ctx, state.record); err != nil {
		return err
	}
	if state.topology != nil {
		if err := state.topology.Cleanup(ctx); err != nil {
			return err
		}
		state.topology = nil
		state.networkNamespace = nil
	}
	return nil
}
