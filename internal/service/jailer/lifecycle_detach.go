package jailer

import "context"

// detachCommittedLaunch ends a successful synchronous launcher without terminating the VM or
// removing its runtime. Later operations must reacquire the durable active record and pidfds.
func (lease *activeLease) detachCommittedLaunch(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "active launch authority is not available")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if ctx == nil {
		return instanceStateError(state.record.vmID, "launch detach context is required")
	}
	if !state.activationComplete || state.record.lifecycle != instanceLifecycleActive || state.vmLock == nil {
		return instanceStateError(state.record.vmID, "durably committed active launch is required for detach")
	}
	// CRITICAL: Post-rename durability errors retain abort authority. Only activation's successful
	// directory sync sets activationComplete and permits relinquishing these live capabilities.
	cleanupCtx := context.WithoutCancel(ctx)
	state.detachStarted = true
	if state.topology != nil {
		if err := state.topology.DetachActive(cleanupCtx); err != nil {
			return appendInstanceOperationError(state.releaseErr, "detach committed VM topology", err)
		}
		state.topology = nil
	}
	if state.managedVMCgroup != nil {
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach active cgroup descriptors",
			state.managedVMCgroup.detachCommittedLaunch(cleanupCtx, state.record.vmID))
		if !state.managedVMCgroup.snapshot().released {
			return state.releaseErr
		}
		state.managedVMCgroup = nil
	}
	if state.runtimeDirectory != nil {
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach active runtime descriptors",
			state.runtimeDirectory.detachCommittedLaunch(cleanupCtx, state.record))
		state.runtimeDirectory.state.mu.Lock()
		released := state.runtimeDirectory.state.released
		state.runtimeDirectory.state.mu.Unlock()
		if !released {
			return state.releaseErr
		}
		state.runtimeDirectory = nil
	}
	if state.persistentOutputs != nil {
		// Private VMM sockets remain available only through root authority after detach.
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach persistent output descriptors",
			state.persistentOutputs.Release(cleanupCtx))
		state.persistentOutputs = nil
	}
	if state.privateRuntimeDirectory != nil {
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach private VMM runtime", state.privateRuntimeDirectory.detachCommittedLaunch(cleanupCtx, state.record))
		state.privateRuntimeDirectory.state.mu.Lock()
		released := state.privateRuntimeDirectory.state.released
		state.privateRuntimeDirectory.state.mu.Unlock()
		if !released {
			return state.releaseErr
		}
		state.privateRuntimeDirectory = nil
	}
	if state.jailDestination != nil {
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach jail destination descriptors",
			state.jailDestination.Release(cleanupCtx))
		state.jailDestination = nil
	}
	if state.processes != nil {
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach active process descriptors",
			state.processes.Release(cleanupCtx))
		state.processes = nil
	}
	// A topology transaction can keep the actual VM lock alive. Preserve all remaining ownership
	// on that rejection so its caller can release topology and retry this same detach.
	if err := state.vmLock.Release(cleanupCtx); err != nil {
		if state.vmLock.isActive() {
			return appendInstanceOperationError(state.releaseErr, "release launch VM lock after topology", err)
		}
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach active VM lock", err)
	}
	state.vmLock = nil
	if state.directories != nil {
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach active authority directories",
			state.directories.Release(cleanupCtx))
		state.directories = nil
	}
	if state.prepared != nil {
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach prepared release authority",
			state.prepared.Release(cleanupCtx))
		state.prepared = nil
	}
	if state.roots != nil {
		state.releaseErr = appendInstanceOperationError(state.releaseErr, "detach authority roots",
			state.roots.Release(cleanupCtx))
		state.roots = nil
	}
	state.networkNamespace = nil
	state.released = true
	return state.releaseErr
}

func (lease *managedVMCgroupLease) detachCommittedLaunch(ctx context.Context, vmID string) error {
	if lease == nil || lease.state == nil {
		return instanceStateError(vmID, "active cgroup authority is not available")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.vmID != vmID || state.verifying || state.removed {
		return instanceStateError(vmID, "active cgroup authority cannot detach")
	}
	state.releaseErr = closeManagedVMCgroupFDs(ctx, state.deps, state.retained)
	state.rootFD = -1
	state.parentFD = -1
	state.leafFD = -1
	state.retained = nil
	state.released = true
	return state.releaseErr
}

func (lease *managedVMRuntimeLease) detachCommittedLaunch(ctx context.Context, record instanceRecord) error {
	if lease == nil || lease.state == nil {
		return instanceStateError(record.vmID, "active runtime authority is not available")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if record.launch == nil {
		return instanceStateError(record.vmID, "runtime launch binding is absent")
	}
	expected := instanceCaller{uid: record.ownerUID, gid: record.launch.ownerGID}
	if state.private {
		expected = instanceCaller{uid: record.launch.executionIdentity.uid, gid: record.launch.executionIdentity.gid}
	}
	if state.vmID != record.vmID || state.caller != expected || state.removed || !state.leafCreated || state.parentSyncPending {
		return instanceStateError(record.vmID, "active runtime authority cannot detach")
	}
	state.releaseErr = closeManagedVMRuntimeFDs(ctx, state.deps, state.retained)
	state.uidDirectoryFD = -1
	state.vmDirectoryFD = -1
	state.retained = nil
	state.released = true
	return state.releaseErr
}
