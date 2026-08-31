package jailer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type reopenedRecordedProcesses struct {
	state *reopenedRecordedProcessesState
}

type reopenedRecordedProcessesState struct {
	// mu guards retained and the complete release lifecycle below. vmID is immutable.
	mu         sync.Mutex
	deps       processRecoveryDeps
	vmID       string
	retained   []recoveredRecordedProcess
	released   bool
	releaseErr error
}

type recoveredRecordedProcess struct {
	role       string
	locator    instanceProcessLocator
	pidfd      int
	signalSent bool
	reaped     bool
}

type processRecoveryDeps struct {
	pidfdOpen       func(context.Context, int) (int, error)
	pidfdAlive      func(context.Context, int) (bool, error)
	descriptorFlags func(context.Context, int) (int, error)
	deriveLocator   func(context.Context, int) (instanceProcessLocator, error)
	terminatePIDFD  func(context.Context, int) error
	waitReapPIDFD   func(context.Context, int) error
	close           func(context.Context, int) error
}

// reopenRecordedProcesses is exclusively for fresh recovery while the record and VM authority are already locked.
// The live activation path receives original pidfds through blockedLaunchProcesses and cannot call this seam.
func reopenRecordedProcesses(
	ctx context.Context,
	deps processRecoveryDeps,
	record instanceRecord,
) (_ *reopenedRecordedProcesses, returnErr error) {
	if err := validateProcessRecoveryDeps(deps); err != nil {
		return nil, processVerificationError(record.vmID, "process recovery dependencies are incomplete", err)
	}
	if ctx == nil {
		return nil, processVerificationError(record.vmID, "process recovery context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, processVerificationError(record.vmID, "reopen recorded processes", err)
	}
	if err := validateInstanceRecord(record); err != nil {
		return nil, err
	}
	if record.launch == nil {
		return nil, processVerificationError(record.vmID, "recorded process locators are absent", nil)
	}
	result := &reopenedRecordedProcesses{state: &reopenedRecordedProcessesState{deps: deps, vmID: record.vmID}}
	committed := false
	defer func() {
		if committed {
			return
		}
		returnErr = appendProcessVerificationError(
			returnErr,
			"release rejected recovered process authority",
			result.Release(context.WithoutCancel(ctx)),
			record.vmID,
		)
	}()
	if err := result.reopen(ctx, record.launch.process, "Firecracker"); err != nil {
		return nil, err
	}
	if record.launch.relay.kind == instanceRelayPresent {
		if err := result.reopen(ctx, record.launch.relay.locator, "relay"); err != nil {
			return nil, err
		}
	}
	committed = true
	return result, nil
}

func validateProcessRecoveryDeps(deps processRecoveryDeps) error {
	if deps.pidfdOpen == nil || deps.pidfdAlive == nil || deps.descriptorFlags == nil ||
		deps.deriveLocator == nil || deps.terminatePIDFD == nil || deps.waitReapPIDFD == nil || deps.close == nil {
		return fmt.Errorf("one or more process recovery effects are unavailable")
	}
	return nil
}

func (processes *reopenedRecordedProcesses) reopen(
	ctx context.Context,
	locator instanceProcessLocator,
	role string,
) (_ error) {
	state := processes.state
	pidfd, err := state.deps.pidfdOpen(ctx, locator.pid)
	if err != nil {
		return processVerificationError(state.vmID, "reopen "+role+" pidfd", err)
	}
	state.retained = append(state.retained, recoveredRecordedProcess{role: role, locator: locator, pidfd: pidfd})
	flags, err := state.deps.descriptorFlags(ctx, pidfd)
	if err != nil {
		return processVerificationError(state.vmID, "inspect recovered "+role+" pidfd", err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return processVerificationError(state.vmID, "recovered "+role+" pidfd does not have close-on-exec", nil)
	}
	alive, err := state.deps.pidfdAlive(ctx, pidfd)
	if err != nil {
		return processVerificationError(state.vmID, "inspect recovered "+role+" pidfd liveness", err)
	}
	if !alive {
		return processVerificationError(state.vmID, "recorded "+role+" process is not live", nil)
	}
	derived, err := state.deps.deriveLocator(ctx, pidfd)
	if err != nil {
		return processVerificationError(state.vmID, "derive recovered "+role+" locator from pidfd", err)
	}
	if derived != locator {
		return processVerificationError(state.vmID, "recovered "+role+" process does not match its locator", nil)
	}
	alive, err = state.deps.pidfdAlive(ctx, pidfd)
	if err != nil {
		return processVerificationError(state.vmID, "recheck recovered "+role+" pidfd liveness", err)
	}
	if !alive {
		return processVerificationError(state.vmID, "recorded "+role+" process exited during recovery", nil)
	}
	return nil
}

func (processes *reopenedRecordedProcesses) terminateAndReap(ctx context.Context) error {
	if processes == nil || processes.state == nil {
		return processVerificationError("", "recovered process authority is not active", nil)
	}
	if ctx == nil {
		return processVerificationError(processes.state.vmID, "recovered process cleanup context is required", nil)
	}
	state := processes.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return processVerificationError(state.vmID, "recovered process authority is not active", nil)
	}
	for index := len(state.retained) - 1; index >= 0; index-- {
		process := &state.retained[index]
		if process.reaped {
			continue
		}
		if !process.signalSent {
			if err := state.deps.terminatePIDFD(ctx, process.pidfd); err != nil {
				return processCleanupRetryableError(
					state.vmID,
					"terminate recovered "+process.role+" through pidfd",
					err,
				)
			}
			process.signalSent = true
		}
		if err := state.deps.waitReapPIDFD(ctx, process.pidfd); err != nil {
			return processCleanupRetryableError(state.vmID, "wait and reap recovered "+process.role, err)
		}
		alive, err := state.deps.pidfdAlive(ctx, process.pidfd)
		if err != nil {
			return processCleanupRetryableError(state.vmID, "verify recovered "+process.role+" exit", err)
		}
		if alive {
			return processCleanupRetryableError(
				state.vmID,
				"verify recovered "+process.role+" exit",
				errors.New("pidfd remains live"),
			)
		}
		process.reaped = true
	}
	return nil
}

func (processes *reopenedRecordedProcesses) verifyLocators(ctx context.Context) error {
	if processes == nil || processes.state == nil {
		return processVerificationError("", "recovered process authority is not active", nil)
	}
	state := processes.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return processVerificationError(state.vmID, "recovered process authority is not active", nil)
	}
	for index := range state.retained {
		process := &state.retained[index]
		derived, err := state.deps.deriveLocator(ctx, process.pidfd)
		if err != nil {
			return processVerificationError(state.vmID, "rederive recovered "+process.role+" locator", err)
		}
		if derived != process.locator {
			return processVerificationError(
				state.vmID,
				"recovered "+process.role+" pidfd locator changed during full verification",
				nil,
			)
		}
		alive, err := state.deps.pidfdAlive(ctx, process.pidfd)
		if err != nil {
			return processVerificationError(state.vmID, "recheck recovered "+process.role+" liveness", err)
		}
		if !alive {
			return processVerificationError(
				state.vmID,
				"recovered "+process.role+" exited during full verification",
				nil,
			)
		}
	}
	return nil
}

func verifyRecoveredActiveIdentity(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	record instanceRecord,
) error {
	if record.launch == nil || record.active == nil {
		return processVerificationError(record.vmID, "active recovery identity is incomplete", nil)
	}
	observed, err := observeProcessIdentity(ctx, deps, record.vmID, "recovered Firecracker", record.launch.process)
	if err != nil {
		return err
	}
	active := record.active
	wantCgroup := infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + record.vmID
	if observed.process.userIDs != active.userIDs || observed.process.groupIDs != active.groupIDs ||
		!slices.Equal(observed.process.supplementaryGroups, active.supplementaryGroups) ||
		observed.process.cgroupPath != wantCgroup || observed.mountNamespace != active.mountNamespace ||
		observed.root != active.jailRoot || !processExecutableMatchesRecordedFirecracker(observed.executable, active.executable) {
		return processVerificationError(record.vmID, "recovered Firecracker does not match full active identity", nil)
	}
	digest, err := hashRecoveredProcessExecutable(ctx, deps, record.vmID, record.launch.process, "Firecracker")
	if err != nil {
		return err
	}
	if digest != active.executable.manifestSHA256 {
		return processVerificationError(record.vmID, "recovered Firecracker digest does not match active identity", nil)
	}
	if active.relay.kind == instanceRelayAbsent {
		if record.launch.relay.kind != instanceRelayAbsent {
			return processVerificationError(record.vmID, "recovered relay presence does not match active identity", nil)
		}
		return nil
	}
	if record.launch.relay.kind != instanceRelayPresent {
		return processVerificationError(record.vmID, "recovered relay locator is absent", nil)
	}
	relay, err := observeProcessIdentity(
		ctx,
		deps,
		record.vmID,
		"recovered relay",
		record.launch.relay.locator,
	)
	if err != nil {
		return err
	}
	if relay.process.userIDs != active.relay.userIDs || relay.process.groupIDs != active.relay.groupIDs ||
		!slices.Equal(relay.process.supplementaryGroups, active.relay.supplementaryGroups) ||
		relay.mountNamespace != active.relay.mountNamespace || relay.root != active.relay.root ||
		!processExecutableMatchesRecordedRelay(relay.executable, active.relay.executable) {
		return processVerificationError(record.vmID, "recovered relay does not match full active identity", nil)
	}
	relayDigest, err := hashRecoveredProcessExecutable(
		ctx,
		deps,
		record.vmID,
		record.launch.relay.locator,
		"relay",
	)
	if err != nil {
		return err
	}
	if relayDigest != active.relay.executable.sha256 {
		return processVerificationError(record.vmID, "recovered relay digest does not match active identity", nil)
	}
	return nil
}

func processExecutableMatchesRecordedFirecracker(
	observed processExecutableDescriptorIdentity,
	expected instanceFirecrackerExecutableIdentity,
) bool {
	return observed.device == expected.device && observed.inode == expected.inode &&
		observed.sizeBytes == expected.sizeBytes && observed.mode == expected.mode && observed.uid == expected.uid &&
		observed.gid == expected.gid && observed.links == expected.links
}

func processExecutableMatchesRecordedRelay(
	observed processExecutableDescriptorIdentity,
	expected instanceRelayExecutableIdentity,
) bool {
	return observed.device == expected.device && observed.inode == expected.inode &&
		observed.sizeBytes == expected.sizeBytes && observed.mode == expected.mode && observed.uid == expected.uid &&
		observed.gid == expected.gid && observed.links == expected.links
}

func hashRecoveredProcessExecutable(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	vmID string,
	locator instanceProcessLocator,
	role string,
) (_ string, returnErr error) {
	descriptors := processVerificationDescriptors{deps: deps, vmID: vmID}
	defer func() { returnErr = descriptors.closeAll(ctx, returnErr) }()
	directoryFD, err := deps.openProcessDirectory(ctx, locator.pid)
	if err != nil {
		return "", processVerificationError(vmID, "open recovered "+role+" process directory for hashing", err)
	}
	descriptors.add(directoryFD)
	if err := requireProcessDescriptorCLOEXEC(
		ctx,
		deps,
		directoryFD,
		"recovered "+role+" process directory",
	); err != nil {
		return "", processVerificationError(vmID, "verify recovered "+role+" process directory", err)
	}
	executableFD, err := deps.pinProcessObject(ctx, directoryFD, processExecutableObject)
	if err != nil {
		return "", processVerificationError(vmID, "pin recovered "+role+" executable for hashing", err)
	}
	descriptors.add(executableFD)
	if err := requireProcessDescriptorCLOEXEC(ctx, deps, executableFD, "recovered "+role+" executable"); err != nil {
		return "", processVerificationError(vmID, "verify recovered "+role+" executable", err)
	}
	before, err := inspectProcessExecutable(ctx, deps, executableFD)
	if err != nil {
		return "", processVerificationError(vmID, "inspect recovered "+role+" executable for hashing", err)
	}
	digest, _, err := hashProcessExecutable(ctx, deps, executableFD, before)
	if err != nil {
		return "", processVerificationError(vmID, "hash recovered "+role+" executable", err)
	}
	return digest, nil
}

func (processes *reopenedRecordedProcesses) Release(ctx context.Context) error {
	if processes == nil || processes.state == nil {
		return nil
	}
	state := processes.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	result := error(nil)
	for index := len(state.retained) - 1; index >= 0; index-- {
		result = appendProcessVerificationError(
			result,
			"close recovered process pidfd",
			state.deps.close(processCleanupContext(ctx), state.retained[index].pidfd),
			state.vmID,
		)
	}
	state.retained = nil
	state.released = true
	state.releaseErr = result
	return state.releaseErr
}
