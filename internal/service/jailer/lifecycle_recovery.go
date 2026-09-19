package jailer

import "context"

type lifecycleRecoveryAdmission struct {
	processDeps  processRecoveryDeps
	identityDeps processIdentityVerifierDeps
	cgroupDeps   managedVMCgroupDeps
	hostBootID   string
}

// beginLifecycleCleanup durably enters cleaning before gathering recoverable
// process evidence. Unknown evidence therefore leaves an explicit retry record.
// A nil lease without an error means the same-owner tombstone is already cleaned.
func (authority *instanceAuthority) beginLifecycleCleanup(
	ctx context.Context,
	caller instanceCaller,
	vmID string,
) (_ *cleanupLease, returnErr error) {
	if ctx == nil {
		return nil, instanceStateError(vmID, "lifecycle cleanup context is required")
	}
	locked, err := authority.lockOwnedInstance(ctx, caller, vmID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if locked != nil {
			returnErr = appendInstanceOperationError(returnErr, "release lifecycle recovery authority", locked.Release(ctx))
		}
	}()
	if locked.record.lifecycle == instanceLifecycleCleaned {
		return nil, nil
	}
	updated, err := beginInstanceCleanup(caller, locked.record)
	if err != nil {
		return nil, err
	}
	var writeErr error
	if updated != locked.record {
		writeErr = locked.directories.writeRecord(ctx, updated)
		if writeErr != nil && !instanceRecordWasReplaced(writeErr) {
			return nil, writeErr
		}
	}
	cleanup := &cleanupLease{state: &cleanupLeaseState{
		roots: locked.roots, directories: locked.directories, vmLock: locked.vmLock, record: updated,
		runtimeRecovery: &managedVMRuntimeRecovery{
			deps: authority.runtimeDeps, policy: managedVMRuntimePolicyFromInstanceAuthority(authority.policy),
		},
		freshRecoveryAdmission: &freshRecoveryAdmission{lifecycle: &lifecycleRecoveryAdmission{
			processDeps: realProcessRecoveryDeps(), identityDeps: realProcessIdentityVerifierDeps(),
			cgroupDeps: authority.cgroupDeps,
		}},
	}}
	locked = nil
	if writeErr != nil {
		return cleanup, writeErr
	}
	return cleanup, cleanup.admitFreshRecovery(ctx)
}

func (lease *cleanupLease) admitLifecycleRecovery(ctx context.Context) error {
	state := lease.state
	state.mu.Lock()
	if state.released || state.vmLock == nil || !state.vmLock.isActive() || state.directories == nil ||
		state.record.lifecycle != instanceLifecycleCleaning || state.freshRecoveryAdmitting {
		state.mu.Unlock()
		return instanceStateError(state.record.vmID, "lifecycle recovery authority is not active")
	}
	if state.freshRecoveryAdmission == nil {
		state.mu.Unlock()
		return nil
	}
	admission := state.freshRecoveryAdmission.lifecycle
	if admission == nil {
		state.mu.Unlock()
		return instanceStateError(state.record.vmID, "lifecycle recovery admission changed")
	}
	record := cloneInstanceRecord(state.record)
	processes := state.recoveredProcesses
	managed := state.managedVMCgroup
	state.freshRecoveryAdmitting = true
	state.mu.Unlock()

	processes, managed, err := admission.admit(ctx, record, processes, managed)
	state.mu.Lock()
	state.recoveredProcesses = processes
	state.managedVMCgroup = managed
	state.freshRecoveryAdmitting = false
	if err == nil {
		state.freshRecoveryAdmission = nil
	}
	state.mu.Unlock()
	return err
}

func (admission *lifecycleRecoveryAdmission) admit(
	ctx context.Context,
	record instanceRecord,
	processes *reopenedRecordedProcesses,
	managed *managedVMCgroupLease,
) (*reopenedRecordedProcesses, *managedVMCgroupLease, error) {
	bootID, err := admission.identityDeps.readBootID(ctx)
	if err != nil || !hostBootIDPattern.MatchString(bootID) {
		return processes, managed, processVerificationError(record.vmID, "read lifecycle recovery host boot ID", err)
	}
	if admission.hostBootID != "" && admission.hostBootID != bootID {
		return processes, managed, processVerificationError(record.vmID, "host boot changed during lifecycle recovery", nil)
	}
	admission.hostBootID = bootID
	if bootID != record.launch.hostBootID {
		// A prior-boot PID is never opened, even to check whether it exists.
		if processes != nil || managed != nil {
			return processes, managed, instanceStateError(record.vmID, "prior-boot cleanup retained current process authority")
		}
		err = requireFreshRecoveryManagedVMCgroupAbsent(ctx, managedVMCgroupReadDepsFrom(admission.cgroupDeps), record.vmID)
		if err == nil {
			err = admission.recheckBoot(ctx, record.vmID)
		}
		return nil, nil, err
	}
	if processes == nil {
		processes, err = reopenLifecycleRecordedProcesses(ctx, admission.processDeps, record)
		if err != nil {
			return processes, managed, err
		}
	}
	observation, live, err := processes.observeLifecycleProcesses(ctx, admission.identityDeps, record)
	if err != nil {
		return processes, managed, err
	}
	if managed == nil {
		managed, _, err = reopenRecoveryManagedVMCgroup(ctx, admission.cgroupDeps, record.vmID)
		if err != nil {
			return processes, managed, err
		}
	}
	if live {
		if managed == nil {
			return processes, nil, instanceStateError(record.vmID, "live recovered VM has no managed cgroup")
		}
		verifier := retainedRecoveredCgroupProcessVerifier{
			processes: processes, locator: record.launch.process, vmID: record.vmID,
		}
		err = managed.VerifyMembership(ctx, record.launch.process, observation, verifier)
	} else if managed != nil {
		err = managed.requireLifecycleRecoveryEmpty(ctx)
	} else {
		err = requireFreshRecoveryManagedVMCgroupAbsent(ctx, managedVMCgroupReadDepsFrom(admission.cgroupDeps), record.vmID)
	}
	if err == nil {
		err = processes.verifyLocators(ctx)
	}
	if err == nil {
		err = admission.recheckBoot(ctx, record.vmID)
	}
	return processes, managed, err
}

func (admission *lifecycleRecoveryAdmission) recheckBoot(ctx context.Context, vmID string) error {
	bootID, err := admission.identityDeps.readBootID(ctx)
	if err != nil || bootID != admission.hostBootID {
		return processVerificationError(vmID, "host boot ID changed during lifecycle recovery admission", err)
	}
	return nil
}
