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
	// A failed absence admission must never authorize deleting a pre-existing tree.
	// A retained handoff proves this operation passed the final fresh-tree check.
	removeTree := state.jailDestination != nil || state.recoveredProcesses != nil || state.record.active != nil
	if state.preparedJailDestination != nil {
		prepared := state.preparedJailDestination.state
		prepared.mu.Lock()
		removeTree = prepared.status == preparedJailDestinationHandoff || prepared.status == preparedJailDestinationTransferred
		prepared.mu.Unlock()
	}
	if removeTree {
		if err := removeStoppedJailTree(ctx, state.record); err != nil {
			return err
		}
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
