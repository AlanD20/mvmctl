package jailer

import "context"

func releaseCleanupManagedVMCgroupLocked(ctx context.Context, state *cleanupLeaseState) error {
	if state.preparedManagedVMCgroup != nil {
		bound, present, err := state.preparedManagedVMCgroup.BindForCleanup(ctx)
		if bound != nil {
			state.managedVMCgroup = bound
			state.preparedManagedVMCgroup = nil
		}
		if err != nil {
			return err
		}
		if present {
			if bound == nil {
				return instanceStateError(state.record.vmID, "cleanup managed VM cgroup admission is incomplete")
			}
			state.managedVMCgroup = bound
			state.preparedManagedVMCgroup = nil
		} else {
			prepared := state.preparedManagedVMCgroup
			releaseErr := prepared.Release(ctx)
			state.preparedManagedVMCgroup = nil
			if releaseErr != nil {
				return appendInstanceOperationError(nil, "release absent cleanup managed VM cgroup", releaseErr)
			}
		}
	}
	if state.managedVMCgroup == nil {
		return nil
	}
	if err := state.managedVMCgroup.Remove(ctx); err != nil {
		return err
	}
	bound := state.managedVMCgroup
	releaseErr := bound.Release(ctx)
	state.managedVMCgroup = nil
	if releaseErr != nil {
		return appendInstanceOperationError(nil, "release cleanup managed VM cgroup", releaseErr)
	}
	return nil
}

// prepareManagedVMCgroup pins the fixed cgroup-v2 root and managed parent while this launch retains its VM lock.
func (lease *launchLease) prepareManagedVMCgroup(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmLock == nil || !state.vmLock.isActive() || state.directories == nil ||
		state.processes == nil || state.preparedManagedVMCgroup != nil || state.managedVMCgroup != nil {
		return instanceStateError(state.record.vmID, "launch lease cannot prepare a managed VM cgroup")
	}
	prepared, err := prepareManagedVMCgroup(ctx, state.cgroupDeps, state.record.vmID)
	if err != nil {
		return err
	}
	state.preparedManagedVMCgroup = prepared
	return nil
}
