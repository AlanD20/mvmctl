package jailer

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

type reopenedRecordedProcesses struct {
	state *reopenedRecordedProcessesState
}

type reopenedRecordedProcessesState struct {
	// mu guards retained and the complete release lifecycle below. vmID is immutable.
	mu         sync.Mutex
	deps       processRecoveryDeps
	vmID       string
	retained   []int
	released   bool
	releaseErr error
}

type processRecoveryDeps struct {
	pidfdOpen            func(context.Context, int) (int, error)
	pidfdAlive           func(context.Context, int) (bool, error)
	descriptorFlags      func(context.Context, int) (int, error)
	openProcessDirectory func(context.Context, int) (int, error)
	readProcessMetadata  func(context.Context, int, processMetadataKind, int) ([]byte, error)
	close                func(context.Context, int) error
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
		deps.openProcessDirectory == nil || deps.readProcessMetadata == nil || deps.close == nil {
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
	state.retained = append(state.retained, pidfd)
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
	directoryFD, err := state.deps.openProcessDirectory(ctx, locator.pid)
	if err != nil {
		return processVerificationError(state.vmID, "open recovered "+role+" process directory", err)
	}
	statRaw, readErr := state.deps.readProcessMetadata(
		ctx,
		directoryFD,
		processStatMetadata,
		maxProcessStatBytes,
	)
	closeErr := state.deps.close(context.WithoutCancel(ctx), directoryFD)
	if readErr != nil || closeErr != nil {
		return appendProcessVerificationError(
			processVerificationError(state.vmID, "read recovered "+role+" process stat", readErr),
			"close recovered process directory",
			closeErr,
			state.vmID,
		)
	}
	startTicks, err := parseProcessStat(statRaw, locator.pid)
	if err != nil {
		return processVerificationError(state.vmID, "parse recovered "+role+" process stat", err)
	}
	if startTicks != locator.startTimeTicks {
		return processVerificationError(state.vmID, "recovered "+role+" process does not match its locator", nil)
	}
	return nil
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
			state.deps.close(processCleanupContext(ctx), state.retained[index]),
			state.vmID,
		)
	}
	state.retained = nil
	state.released = true
	state.releaseErr = result
	return state.releaseErr
}
