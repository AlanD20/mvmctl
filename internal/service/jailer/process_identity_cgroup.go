package jailer

import "context"

type retainedLaunchCgroupProcessVerifier struct {
	deps    processIdentityVerifierDeps
	pidfd   int
	locator instanceProcessLocator
	vmID    string
}

func (verifier retainedLaunchCgroupProcessVerifier) verifyBeforeCgroupMembership(
	ctx context.Context,
	locator instanceProcessLocator,
	_ processObservation,
) error {
	return verifier.verify(ctx, locator)
}

func (verifier retainedLaunchCgroupProcessVerifier) verifyAfterCgroupMembership(
	ctx context.Context,
	locator instanceProcessLocator,
	_ processObservation,
) error {
	return verifier.verify(ctx, locator)
}

func (verifier retainedLaunchCgroupProcessVerifier) verify(
	ctx context.Context,
	locator instanceProcessLocator,
) error {
	if locator != verifier.locator {
		return processVerificationError(verifier.vmID, "cgroup verifier locator does not match launch authority", nil)
	}
	if err := requireProcessAlive(
		ctx,
		verifier.deps.pidfdAlive,
		verifier.pidfd,
		verifier.vmID,
		"Firecracker",
	); err != nil {
		return err
	}
	return requireExactPIDFDLocator(ctx, verifier.deps, verifier.pidfd, verifier.locator, verifier.vmID, "Firecracker")
}

func bindOrVerifyLaunchManagedVMCgroup(
	ctx context.Context,
	state *launchLeaseState,
	deps processIdentityVerifierDeps,
	record instanceRecord,
	processState *blockedLaunchProcessesState,
	verified verifiedLaunchIdentity,
) error {
	cgroupVerifier := retainedLaunchCgroupProcessVerifier{
		deps: deps, pidfd: processState.firecracker.pidfd, locator: record.launch.process, vmID: record.vmID,
	}
	if state.preparedManagedVMCgroup != nil {
		bound, bindErr := state.preparedManagedVMCgroup.Bind(
			ctx,
			record.launch.process,
			verified.finalFirecracker,
			cgroupVerifier,
		)
		if bound != nil {
			state.managedVMCgroup = bound
			state.preparedManagedVMCgroup = nil
		}
		if bindErr != nil {
			return bindErr
		}
		if bound == nil {
			return processVerificationError(record.vmID, "managed VM cgroup binding is incomplete", nil)
		}
	} else if err := state.managedVMCgroup.VerifyMembership(
		ctx,
		record.launch.process,
		verified.finalFirecracker,
		cgroupVerifier,
	); err != nil {
		return err
	}
	return nil
}
