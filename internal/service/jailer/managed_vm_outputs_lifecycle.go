package jailer

import "context"

// preparePersistentOutputs consumes a dedicated pinned cache lease after successful durable launch registration.
// CRITICAL: VM identity, operator, execution UID and cache authority derive from the durable record; paths do not
// cross this seam. The base launch resource lease independently owns its cache pin.
func (lease *launchLease) preparePersistentOutputs(
	ctx context.Context,
	cache *managedCacheLease,
	metrics managedPersistentMetricsPresence,
) error {
	return preparePersistentOutputsWithDeps(ctx, lease, cache, metrics, realManagedPersistentOutputDeps())
}

func preparePersistentOutputsWithDeps(
	ctx context.Context,
	lease *launchLease,
	cache *managedCacheLease,
	metrics managedPersistentMetricsPresence,
	deps managedPersistentOutputDeps,
) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.registrationComplete || state.vmLock == nil || !state.vmLock.isActive() ||
		state.directories == nil || state.prepared == nil || state.processes == nil || state.processes.state == nil {
		return instanceStateError(state.record.vmID, "launch lease cannot prepare persistent outputs")
	}
	if state.persistentOutputs != nil {
		conflict := instanceStateError(state.record.vmID, "launch already has a persistent output preparation attempt")
		if state.persistentOutputs.state == nil {
			return conflict
		}
		outputState := state.persistentOutputs.state
		outputState.mu.Lock()
		defer outputState.mu.Unlock()
		return annotatePersistentOutputError(conflict, outputState)
	}
	if ctx == nil {
		return instanceStateError(state.record.vmID, "persistent output context is required")
	}
	if err := ctx.Err(); err != nil {
		return persistentOutputError("prepare persistent outputs", err)
	}
	if err := validateInstanceRecord(state.record); err != nil {
		return err
	}
	if state.record.lifecycle != instanceLifecycleLaunching || state.record.launch == nil {
		return instanceStateError(state.record.vmID, "launch authority is not awaiting persistent output preparation")
	}
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return err
	}
	if cache.ownerUID != state.record.ownerUID || cache.identity != state.record.launch.cacheIdentity {
		return instanceStateError(state.record.vmID, "persistent output cache does not match launch authority")
	}
	if err := validateManagedPersistentOutputDeps(deps); err != nil {
		return err
	}
	outputs, err := managedPersistentOutputNames(metrics)
	if err != nil {
		return err
	}
	processState := state.processes.state
	processState.mu.Lock()
	defer processState.mu.Unlock()
	if processState.status != blockedLaunchProcessesOpen {
		return instanceStateError(state.record.vmID, "persistent outputs require both children to remain blocked")
	}

	// Ownership transfers before admission opens descendants, so even partial preparation remains owned by abort.
	ownedCache := *cache
	cache.retained = nil
	cache.cacheFD = -1
	cache.ownerUID = 0
	cache.identity = managedCacheIdentity{}
	cache.configDeps = managedFirecrackerConfigDeps{}
	outputState := &managedVMPersistentOutputState{
		deps:          deps,
		cache:         &ownedCache,
		owner:         instanceCaller{uid: state.record.ownerUID, gid: state.record.launch.ownerGID},
		executionUID:  state.record.launch.executionIdentity.uid,
		vmDirectoryFD: -1,
		outputs:       outputs,
	}
	state.persistentOutputs = &managedVMPersistentOutputLease{state: outputState}
	outputState.mu.Lock()
	defer outputState.mu.Unlock()
	if err := admitManagedPersistentOutputs(ctx, outputState, state.record.vmID); err != nil {
		return annotatePersistentOutputError(err, outputState)
	}
	return annotatePersistentOutputError(commitPersistentOutputs(ctx, outputState), outputState)
}

// CALLER MUST HOLD: state.mu. Completed processes cannot write through these descriptors again; published files stay.
func releaseCleanupPersistentOutputsLocked(ctx context.Context, state *cleanupLeaseState) error {
	if state.persistentOutputs == nil {
		return nil
	}
	outputs := state.persistentOutputs
	if outputs.state == nil {
		return instanceStateError(state.record.vmID, "persistent output cleanup authority is incomplete")
	}
	outputs.state.mu.Lock()
	released := outputs.state.released
	outputs.state.mu.Unlock()
	// The durable record's exact-inode revocation has already succeeded. A copied lease may have
	// closed its descriptors earlier; those descriptors cannot be safely reused or revoked again.
	if !released {
		if err := revokeIsolatedPersistentOutputLease(ctx, outputs); err != nil {
			return err
		}
	}
	state.persistentOutputs = nil
	// A terminal close error must surface once without retrying a descriptor that the OS may already have reused.
	return outputs.Release(context.WithoutCancel(ctx))
}
