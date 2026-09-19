package jailer

import (
	"context"
	"errors"
	"slices"

	"golang.org/x/sys/unix"
)

// Recovered exit observations carry no signal authority. A live reused PID
// rejects the entire admission without signaling any member of the process set.
func reopenLifecycleRecordedProcesses(
	ctx context.Context,
	deps processRecoveryDeps,
	record instanceRecord,
) (_ *reopenedRecordedProcesses, returnErr error) {
	if err := validateProcessRecoveryDeps(deps); err != nil {
		return nil, processVerificationError(record.vmID, "lifecycle process recovery is unavailable", err)
	}
	processes := &reopenedRecordedProcesses{state: &reopenedRecordedProcessesState{deps: deps, vmID: record.vmID}}
	defer func() {
		if returnErr != nil {
			returnErr = appendProcessVerificationError(returnErr, "close rejected lifecycle processes",
				processes.Release(context.WithoutCancel(ctx)), record.vmID)
		}
	}()
	if err := processes.reopenLifecycleProcess(ctx, record.launch.process, "Firecracker"); err != nil {
		return nil, err
	}
	if record.launch.relay.kind == instanceRelayPresent {
		if err := processes.reopenLifecycleProcess(ctx, record.launch.relay.locator, "relay"); err != nil {
			return nil, err
		}
	}
	return processes, nil
}

func (processes *reopenedRecordedProcesses) reopenLifecycleProcess(
	ctx context.Context,
	locator instanceProcessLocator,
	role string,
) error {
	state := processes.state
	fd, err := state.deps.pidfdOpen(ctx, locator.pid)
	if errors.Is(err, unix.ESRCH) {
		state.retained = append(state.retained, recoveredRecordedProcess{
			role: role, locator: locator, pidfd: -1, reaped: true,
		})
		return nil
	}
	if err != nil {
		return processVerificationError(state.vmID, "reopen lifecycle "+role+" pidfd", err)
	}
	state.retained = append(state.retained, recoveredRecordedProcess{role: role, locator: locator, pidfd: fd})
	process := &state.retained[len(state.retained)-1]
	flags, err := state.deps.descriptorFlags(ctx, fd)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return processVerificationError(state.vmID, "lifecycle pidfd is not close-on-exec", err)
	}
	return refreshLifecycleProcess(ctx, state, process)
}

func refreshLifecycleProcess(
	ctx context.Context,
	state *reopenedRecordedProcessesState,
	process *recoveredRecordedProcess,
) error {
	if process.reaped {
		return nil
	}
	alive, err := state.deps.pidfdAlive(ctx, process.pidfd)
	if err != nil {
		return processVerificationError(state.vmID, "inspect lifecycle "+process.role+" liveness", err)
	}
	if !alive {
		if err := state.deps.waitReapPIDFD(ctx, process.pidfd); err != nil {
			return processVerificationError(state.vmID, "reap exited lifecycle "+process.role, err)
		}
		process.reaped = true
		return nil
	}
	derived, deriveErr := state.deps.deriveLocator(ctx, process.pidfd)
	if deriveErr == nil && derived != process.locator {
		return processVerificationError(state.vmID, "lifecycle "+process.role+" PID was reused", nil)
	}
	alive, err = state.deps.pidfdAlive(ctx, process.pidfd)
	if err != nil {
		return processVerificationError(state.vmID, "recheck lifecycle "+process.role+" liveness", err)
	}
	if !alive {
		if err := state.deps.waitReapPIDFD(ctx, process.pidfd); err != nil {
			return processVerificationError(state.vmID, "reap exited lifecycle "+process.role, err)
		}
		process.reaped = true
		return nil
	}
	if deriveErr != nil {
		return processVerificationError(state.vmID, "derive lifecycle "+process.role+" locator", deriveErr)
	}
	return nil
}

func (processes *reopenedRecordedProcesses) observeLifecycleProcesses(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	record instanceRecord,
) (processObservation, bool, error) {
	state := processes.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.verifying {
		return processObservation{}, false, processVerificationError(record.vmID, "lifecycle process authority is busy", nil)
	}
	var observation processObservation
	var firecrackerLive bool
	for index := range state.retained {
		process := &state.retained[index]
		if err := refreshLifecycleProcess(ctx, state, process); err != nil {
			return processObservation{}, false, err
		}
		if process.reaped {
			continue
		}
		if record.active == nil {
			return processObservation{}, false, processVerificationError(record.vmID,
				"live launching process has no committed executable and namespace identity", nil)
		}
		if process.role == "Firecracker" {
			// Relay admission is independent, so a dead relay does not prevent
			// the remaining exact Firecracker task from being stopped.
			firecrackerRecord := cloneInstanceRecord(record)
			firecrackerRecord.launch.relay = instanceLaunchRelay{kind: instanceRelayAbsent}
			firecrackerRecord.active.relay = instanceActiveRelay{kind: instanceRelayAbsent}
			var err error
			observation, err = verifyRecoveredActiveIdentity(ctx, deps, firecrackerRecord)
			if err != nil {
				return processObservation{}, false, err
			}
			firecrackerLive = true
		} else if err := verifyLifecycleRelayIdentity(ctx, deps, record); err != nil {
			return processObservation{}, false, err
		}
	}
	return observation, firecrackerLive, nil
}

func verifyLifecycleRelayIdentity(ctx context.Context, deps processIdentityVerifierDeps, record instanceRecord) error {
	locator := record.launch.relay.locator
	expected := record.active.relay
	observed, err := observeProcessIdentity(ctx, deps, record.vmID, "recovered relay", locator)
	if err != nil {
		return err
	}
	if expected.kind != instanceRelayPresent || observed.process.userIDs != expected.userIDs ||
		observed.process.groupIDs != expected.groupIDs ||
		!slices.Equal(observed.process.supplementaryGroups, expected.supplementaryGroups) ||
		observed.mountNamespace != expected.mountNamespace || observed.root != expected.root ||
		!processExecutableMatchesRecordedRelay(observed.executable, expected.executable) {
		return processVerificationError(record.vmID, "recovered relay does not match committed identity", nil)
	}
	digest, err := hashRecoveredProcessExecutable(ctx, deps, record.vmID, locator, "relay")
	if err != nil {
		return err
	}
	if digest != expected.executable.sha256 {
		return processVerificationError(record.vmID, "recovered relay digest does not match committed identity", nil)
	}
	return nil
}
