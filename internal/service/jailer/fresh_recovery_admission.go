package jailer

import "context"

type freshRecoveryAdmission struct {
	sourceLifecycle instanceLifecycle
	hostBootID      string
	observation     processObservation
	locator         instanceProcessLocator
	cgroupDeps      managedVMCgroupDeps
	identityDeps    processIdentityVerifierDeps
	processVerifier retainedRecoveredCgroupProcessVerifier
}

// finishFreshRecoveryAdmissionLocked completes the evidence chain that began before the cleaning record was durable.
// A failed proof leaves this exact admission, process authority, and any admitted cgroup lease owned by cleanup so a
// later Complete call can retry without reconstructing authority.
//
// CALLER MUST HOLD: state.mu.
func finishFreshRecoveryAdmissionLocked(ctx context.Context, state *cleanupLeaseState) error {
	admission := state.freshRecoveryAdmission
	if admission == nil {
		return nil
	}
	if state.recoveredProcesses == nil || admission.processVerifier.processes != state.recoveredProcesses ||
		admission.processVerifier.locator != admission.locator || admission.processVerifier.vmID != state.record.vmID {
		return instanceStateError(state.record.vmID, "fresh-recovery admission authority is inconsistent")
	}

	if state.managedVMCgroup == nil {
		cgroup, present, err := recoverManagedVMCgroup(
			ctx,
			admission.cgroupDeps,
			state.record.vmID,
			admission.locator,
			admission.observation,
			admission.processVerifier,
		)
		if cgroup != nil {
			state.managedVMCgroup = cgroup
		}
		if err != nil {
			return err
		}
		if present && cgroup == nil {
			return instanceStateError(state.record.vmID, "fresh-recovery managed VM cgroup admission is incomplete")
		}
		if !present && admission.sourceLifecycle == instanceLifecycleActive {
			return instanceStateError(state.record.vmID, "active fresh recovery has no managed VM cgroup")
		}
	} else if err := state.managedVMCgroup.VerifyMembership(
		ctx,
		admission.locator,
		admission.observation,
		admission.processVerifier,
	); err != nil {
		return err
	}

	if err := state.recoveredProcesses.verifyLocators(ctx); err != nil {
		return err
	}
	bootID, err := admission.identityDeps.readBootID(ctx)
	if err != nil {
		return processVerificationError(state.record.vmID, "recheck host boot ID after fresh recovery admission", err)
	}
	if bootID != admission.hostBootID {
		return processVerificationError(state.record.vmID, "host boot ID changed during fresh recovery admission", nil)
	}
	state.freshRecoveryAdmission = nil
	return nil
}
