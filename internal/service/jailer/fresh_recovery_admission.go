package jailer

import (
	"context"
	"errors"
)

type freshRecoveryAdmission struct {
	lifecycle       *lifecycleRecoveryAdmission
	sourceLifecycle instanceLifecycle
	hostBootID      string
	observation     processObservation
	locator         instanceProcessLocator
	cgroupDeps      managedVMCgroupDeps
	identityDeps    processIdentityVerifierDeps
	processVerifier retainedRecoveredCgroupProcessVerifier
}

// admitFreshRecovery completes the evidence chain that began before the cleaning record was durable. Callbacks run
// without the cleanup mutex. A failed proof leaves the exact admission and every authority obtained during the
// attempt owned by cleanup so Complete can retry without reconstructing them.
func (lease *cleanupLease) admitFreshRecovery(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "cleanup lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	vmID := state.record.vmID
	if ctx == nil {
		state.mu.Unlock()
		return instanceStateError(vmID, "fresh-recovery admission context is required")
	}
	if state.released || state.vmLock == nil || !state.vmLock.isActive() || state.directories == nil {
		state.mu.Unlock()
		return instanceStateError(vmID, "cleanup lease is not active")
	}
	if state.freshRecoveryAdmitting {
		state.mu.Unlock()
		return instanceStateError(vmID, "fresh-recovery admission is already in progress")
	}
	admission := state.freshRecoveryAdmission
	if admission == nil {
		state.mu.Unlock()
		return nil
	}
	if admission.lifecycle != nil {
		state.mu.Unlock()
		return lease.admitLifecycleRecovery(ctx)
	}
	recovered := state.recoveredProcesses
	if recovered == nil || admission.processVerifier.processes != recovered ||
		admission.processVerifier.locator != admission.locator || admission.processVerifier.vmID != vmID ||
		(state.managedVMCgroup != nil && state.preparedManagedVMCgroup != nil) {
		state.mu.Unlock()
		return instanceStateError(vmID, "fresh-recovery admission authority is inconsistent")
	}
	managed := state.managedVMCgroup
	prepared := state.preparedManagedVMCgroup
	state.freshRecoveryAdmitting = true
	state.mu.Unlock()

	var admissionErr error
	if managed != nil {
		admissionErr = managed.VerifyMembership(
			ctx,
			admission.locator,
			admission.observation,
			admission.processVerifier,
		)
	} else if prepared == nil {
		var present bool
		managed, present, admissionErr = recoverManagedVMCgroup(
			ctx,
			admission.cgroupDeps,
			vmID,
			admission.locator,
			admission.observation,
			admission.processVerifier,
		)
		if admissionErr == nil && present && managed == nil {
			admissionErr = instanceStateError(vmID, "fresh-recovery managed VM cgroup admission is incomplete")
		}
		if admissionErr == nil && !present {
			if admission.sourceLifecycle == instanceLifecycleActive {
				admissionErr = instanceStateError(vmID, "active fresh recovery has no managed VM cgroup")
			} else {
				prepared, admissionErr = prepareFreshRecoveryManagedVMCgroup(
					ctx,
					admission.cgroupDeps,
					vmID,
				)
			}
		}
	}
	if admissionErr == nil {
		admissionErr = recovered.verifyLocators(ctx)
	}
	if admissionErr == nil {
		var bootID string
		bootID, admissionErr = admission.identityDeps.readBootID(ctx)
		if admissionErr != nil {
			admissionErr = processVerificationError(
				vmID,
				"recheck host boot ID after fresh recovery admission",
				admissionErr,
			)
		} else if bootID != admission.hostBootID {
			admissionErr = processVerificationError(
				vmID,
				"host boot ID changed during fresh recovery admission",
				nil,
			)
		}
	}

	state.mu.Lock()
	if state.managedVMCgroup == nil && managed != nil {
		state.managedVMCgroup = managed
	} else if managed != nil && state.managedVMCgroup != managed {
		admissionErr = errors.Join(
			admissionErr,
			instanceStateError(vmID, "fresh-recovery managed cgroup authority changed"),
		)
	}
	if state.preparedManagedVMCgroup == nil && prepared != nil {
		state.preparedManagedVMCgroup = prepared
	} else if prepared != nil && state.preparedManagedVMCgroup != prepared {
		admissionErr = errors.Join(
			admissionErr,
			instanceStateError(vmID, "fresh-recovery prepared cgroup authority changed"),
		)
	}
	state.freshRecoveryAdmitting = false
	if admissionErr == nil {
		state.freshRecoveryAdmission = nil
	}
	state.mu.Unlock()
	return admissionErr
}
