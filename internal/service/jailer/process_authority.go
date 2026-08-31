package jailer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

type blockedLaunchProcesses struct {
	state *blockedLaunchProcessesState
}

type blockedLaunchProcessesState struct {
	// mu guards every process descriptor, the pinned receiver executable, and the complete lifecycle below.
	mu                 sync.Mutex
	deps               blockedLaunchProcessDeps
	vmID               string
	firecracker        blockedLaunchProcess
	relay              *blockedLaunchProcess
	receiverExecutable *system.PinnedExecutable
	status             blockedLaunchProcessStatus
	releaseErr         error
}

type blockedLaunchProcess struct {
	locator    instanceProcessLocator
	pidfd      int
	gate       *blockedLaunchGate
	signalSent bool
	reaped     bool
}

type blockedLaunchProcessSource struct {
	pidfd int
	gate  *blockedLaunchGate
}

type blockedLaunchProcessStatus uint8

const (
	blockedLaunchProcessesOpen blockedLaunchProcessStatus = iota + 1
	blockedLaunchProcessesTransferred
	blockedLaunchProcessesReleasedToJailer
	blockedLaunchProcessesReleased
)

type blockedLaunchProcessDeps struct {
	pidfdAlive                func(context.Context, int) (bool, error)
	descriptorFlags           func(context.Context, int) (int, error)
	deriveLocator             func(context.Context, int) (instanceProcessLocator, error)
	pinnedExecutableReinspect func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error)
	releasePinnedExecutable   func(context.Context, *system.PinnedExecutable) error
	terminatePIDFD            func(context.Context, int) error
	waitReapPIDFD             func(context.Context, int) error
	close                     func(context.Context, int) error
}

type launchProcessLocators struct {
	process instanceProcessLocator
	relay   instanceLaunchRelay
}

func newBlockedLaunchProcesses(
	ctx context.Context,
	deps blockedLaunchProcessDeps,
	vmID string,
	firecrackerSource blockedLaunchProcessSource,
	relaySource *blockedLaunchProcessSource,
	receiverExecutable *system.PinnedExecutable,
) (_ *blockedLaunchProcesses, returnErr error) {
	if err := validateBlockedLaunchProcessDeps(deps); err != nil {
		return nil, processAuthorityError(vmID, "blocked launch process dependencies are incomplete", err)
	}
	if ctx == nil {
		return nil, processAuthorityError(vmID, "blocked launch process context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, processAuthorityError(vmID, "admit blocked launch processes", err)
	}
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("vm ID is invalid")
	}
	capability := &blockedLaunchProcesses{state: &blockedLaunchProcessesState{
		deps: deps, vmID: vmID, firecracker: blockedLaunchProcess{pidfd: -1},
		receiverExecutable: receiverExecutable, status: blockedLaunchProcessesOpen,
	}}
	committed := false
	defer func() {
		if committed {
			return
		}
		returnErr = appendBlockedLaunchProcessCleanupError(
			returnErr,
			"release rejected blocked launch process authority",
			capability.Release(context.WithoutCancel(ctx)),
			vmID,
		)
	}()
	firecracker, err := admitBlockedProcess(ctx, deps, firecrackerSource, "Firecracker")
	if err != nil {
		return nil, processAuthorityError(vmID, "admit blocked Firecracker process", err)
	}
	capability.state.firecracker = firecracker
	if relaySource != nil {
		relay, relayErr := admitBlockedProcess(ctx, deps, *relaySource, "relay")
		if relayErr != nil {
			return nil, processAuthorityError(vmID, "admit blocked relay process", relayErr)
		}
		capability.state.relay = &relay
		if relay.locator.pid == firecracker.locator.pid {
			return nil, instanceValidationError("relay PID must differ from Firecracker PID")
		}
		if relay.pidfd == firecracker.pidfd {
			return nil, processAuthorityError(vmID, "blocked process pidfds must be distinct", nil)
		}
	}
	if receiverExecutable == nil {
		return nil, processAuthorityError(vmID, "pinned receiver executable is required", nil)
	}
	if _, err := deps.pinnedExecutableReinspect(ctx, receiverExecutable); err != nil {
		return nil, processAuthorityError(vmID, "admit pinned receiver executable", err)
	}
	committed = true
	return capability, nil
}

func cloneBlockedLaunchProcess(process *blockedLaunchProcess) *blockedLaunchProcess {
	if process == nil {
		return nil
	}
	cloned := *process
	return &cloned
}

func validateBlockedLaunchProcessDeps(deps blockedLaunchProcessDeps) error {
	if deps.pidfdAlive == nil || deps.descriptorFlags == nil || deps.deriveLocator == nil ||
		deps.pinnedExecutableReinspect == nil ||
		deps.releasePinnedExecutable == nil || deps.terminatePIDFD == nil || deps.waitReapPIDFD == nil ||
		deps.close == nil {
		return fmt.Errorf("one or more blocked process effects are unavailable")
	}
	return nil
}

func admitBlockedProcess(
	ctx context.Context,
	deps blockedLaunchProcessDeps,
	source blockedLaunchProcessSource,
	role string,
) (_ blockedLaunchProcess, returnErr error) {
	process := blockedLaunchProcess{pidfd: source.pidfd, gate: source.gate}
	if process.pidfd < 0 || process.gate == nil || process.gate.state == nil {
		return blockedLaunchProcess{}, fmt.Errorf("%s pidfd or gate is invalid", role)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanupCtx := processCleanupContext(ctx)
		returnErr = errors.Join(
			returnErr,
			process.gate.Release(cleanupCtx),
			deps.close(cleanupCtx, process.pidfd),
		)
	}()
	flags, err := deps.descriptorFlags(ctx, process.pidfd)
	if err != nil {
		return blockedLaunchProcess{}, fmt.Errorf("inspect %s pidfd flags: %w", role, err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return blockedLaunchProcess{}, fmt.Errorf("%s pidfd does not have close-on-exec", role)
	}
	alive, err := deps.pidfdAlive(ctx, process.pidfd)
	if err != nil {
		return blockedLaunchProcess{}, fmt.Errorf("inspect %s pidfd liveness: %w", role, err)
	}
	if !alive {
		return blockedLaunchProcess{}, fmt.Errorf("%s process exited before registration", role)
	}
	if err := process.gate.challengeAndAcknowledge(ctx); err != nil {
		return blockedLaunchProcess{}, fmt.Errorf("challenge blocked %s process: %w", role, err)
	}
	locator, err := deps.deriveLocator(ctx, process.pidfd)
	if err != nil {
		return blockedLaunchProcess{}, fmt.Errorf("derive %s locator from retained pidfd: %w", role, err)
	}
	if err := validateInstanceProcessLocator(locator); err != nil {
		return blockedLaunchProcess{}, err
	}
	process.locator = locator
	alive, err = deps.pidfdAlive(ctx, process.pidfd)
	if err != nil {
		return blockedLaunchProcess{}, fmt.Errorf("reinspect %s pidfd liveness: %w", role, err)
	}
	if !alive {
		return blockedLaunchProcess{}, fmt.Errorf("%s process exited during blocked admission", role)
	}
	committed = true
	return process, nil
}

func (processes *blockedLaunchProcesses) lockForRegistration(
	ctx context.Context,
	vmID string,
) (*blockedLaunchProcessesState, launchProcessLocators, error) {
	if processes == nil || processes.state == nil {
		return nil, launchProcessLocators{}, processAuthorityError(
			vmID,
			"blocked launch process authority is not active",
			nil,
		)
	}
	state := processes.state
	state.mu.Lock()
	if state.status != blockedLaunchProcessesOpen || state.receiverExecutable == nil {
		state.mu.Unlock()
		return nil, launchProcessLocators{}, processAuthorityError(
			vmID,
			"blocked launch process authority is not active",
			nil,
		)
	}
	if state.vmID != vmID {
		state.mu.Unlock()
		return nil, launchProcessLocators{}, processAuthorityError(
			vmID,
			"blocked launch process VM binding is invalid",
			nil,
		)
	}
	if ctx == nil {
		state.mu.Unlock()
		return nil, launchProcessLocators{}, processAuthorityError(
			vmID,
			"blocked launch process context is required",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return nil, launchProcessLocators{}, processAuthorityError(vmID, "reinspect blocked launch processes", err)
	}
	if err := validateRetainedBlockedProcess(ctx, state.deps, state.firecracker, "Firecracker"); err != nil {
		state.mu.Unlock()
		return nil, launchProcessLocators{}, processAuthorityError(vmID, "reinspect blocked Firecracker process", err)
	}
	relay := instanceLaunchRelay{kind: instanceRelayAbsent}
	if state.relay != nil {
		if err := validateRetainedBlockedProcess(ctx, state.deps, *state.relay, "relay"); err != nil {
			state.mu.Unlock()
			return nil, launchProcessLocators{}, processAuthorityError(vmID, "reinspect blocked relay process", err)
		}
		relay = instanceLaunchRelay{kind: instanceRelayPresent, locator: state.relay.locator}
	}
	if _, err := state.deps.pinnedExecutableReinspect(ctx, state.receiverExecutable); err != nil {
		state.mu.Unlock()
		return nil, launchProcessLocators{}, processAuthorityError(vmID, "reinspect pinned receiver executable", err)
	}
	return state, launchProcessLocators{process: state.firecracker.locator, relay: relay}, nil
}

func validateRetainedBlockedProcess(
	ctx context.Context,
	deps blockedLaunchProcessDeps,
	process blockedLaunchProcess,
	role string,
) error {
	if err := validateInstanceProcessLocator(process.locator); err != nil {
		return err
	}
	if process.pidfd < 0 || process.gate == nil || process.gate.state == nil || process.gate.isReleased() {
		return fmt.Errorf("%s retained pidfd or gate is invalid", role)
	}
	flags, err := deps.descriptorFlags(ctx, process.pidfd)
	if err != nil {
		return fmt.Errorf("inspect %s pidfd flags: %w", role, err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return fmt.Errorf("%s pidfd does not have close-on-exec", role)
	}
	derived, err := deps.deriveLocator(ctx, process.pidfd)
	if err != nil {
		return fmt.Errorf("rederive %s locator from retained pidfd: %w", role, err)
	}
	if derived != process.locator {
		return fmt.Errorf("%s retained pidfd locator changed", role)
	}
	alive, err := deps.pidfdAlive(ctx, process.pidfd)
	if err != nil {
		return fmt.Errorf("inspect %s pidfd liveness: %w", role, err)
	}
	if !alive {
		return fmt.Errorf("%s process exited before registration", role)
	}
	return nil
}

func releaseRetainedLaunchProcessesToJailerLocked(
	ctx context.Context,
	state *blockedLaunchProcessesState,
	recheckDestination func(context.Context) error,
) error {
	if state.status != blockedLaunchProcessesOpen {
		return processAuthorityError(state.vmID, "blocked launch process authority is not ready for handoff", nil)
	}
	if state.relay != nil {
		if err := state.relay.gate.release(ctx); err != nil {
			return processAuthorityError(state.vmID, "release blocked relay process", err)
		}
	}
	if err := recheckDestination(ctx); err != nil {
		return err
	}
	if err := state.firecracker.gate.release(ctx); err != nil {
		return processAuthorityError(state.vmID, "release blocked Firecracker process", err)
	}
	state.status = blockedLaunchProcessesReleasedToJailer
	return nil
}

func transferBlockedLaunchProcessesLocked(state *blockedLaunchProcessesState) *blockedLaunchProcesses {
	target := &blockedLaunchProcesses{state: &blockedLaunchProcessesState{
		deps: state.deps, vmID: state.vmID, firecracker: state.firecracker, relay: state.relay,
		receiverExecutable: state.receiverExecutable, status: blockedLaunchProcessesOpen,
	}}
	state.firecracker = blockedLaunchProcess{pidfd: -1}
	state.relay = nil
	state.receiverExecutable = nil
	state.status = blockedLaunchProcessesTransferred
	return target
}

func (processes *blockedLaunchProcesses) terminateAndReap(ctx context.Context) error {
	if processes == nil || processes.state == nil {
		return processAuthorityError("", "cleanup process authority is not active", nil)
	}
	if ctx == nil {
		return processAuthorityError(processes.state.vmID, "cleanup process context is required", nil)
	}
	state := processes.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status != blockedLaunchProcessesOpen && state.status != blockedLaunchProcessesReleasedToJailer {
		return processAuthorityError(state.vmID, "cleanup process authority is not active", nil)
	}
	if state.relay != nil {
		if err := terminateAndReapBlockedProcess(ctx, state, state.relay, "relay"); err != nil {
			return err
		}
	}
	return terminateAndReapBlockedProcess(ctx, state, &state.firecracker, "Firecracker")
}

func terminateAndReapBlockedProcess(
	ctx context.Context,
	state *blockedLaunchProcessesState,
	process *blockedLaunchProcess,
	role string,
) error {
	if process == nil || process.pidfd < 0 {
		return processAuthorityError(state.vmID, role+" cleanup pidfd is invalid", nil)
	}
	if process.reaped {
		return nil
	}
	if !process.signalSent {
		if err := state.deps.terminatePIDFD(ctx, process.pidfd); err != nil {
			return processCleanupRetryableError(state.vmID, "terminate "+role+" through pidfd", err)
		}
		process.signalSent = true
	}
	if err := state.deps.waitReapPIDFD(ctx, process.pidfd); err != nil {
		return processCleanupRetryableError(state.vmID, "wait and reap "+role+" through pidfd", err)
	}
	alive, err := state.deps.pidfdAlive(ctx, process.pidfd)
	if err != nil {
		return processCleanupRetryableError(state.vmID, "verify "+role+" pidfd exit", err)
	}
	if alive {
		return processCleanupRetryableError(state.vmID, "verify "+role+" pidfd exit", errors.New("pidfd remains live"))
	}
	process.reaped = true
	return nil
}

func processCleanupRetryableError(vmID, message string, cause error) *errs.DomainError {
	options := []errs.ErrorOption{errs.WithClass(errs.ClassRetryable)}
	if vmID != "" {
		options = append(options, errs.WithEntity(vmID))
	}
	return errs.WrapMsg(errs.CodeProcessError, message+": "+cause.Error(), cause, options...)
}

func (processes *blockedLaunchProcesses) Release(ctx context.Context) error {
	if processes == nil || processes.state == nil {
		return nil
	}
	state := processes.state
	state.mu.Lock()
	defer state.mu.Unlock()
	switch state.status {
	case blockedLaunchProcessesTransferred, blockedLaunchProcessesReleased:
		return state.releaseErr
	case blockedLaunchProcessesOpen, blockedLaunchProcessesReleasedToJailer:
	default:
		return processAuthorityError(state.vmID, "blocked launch process authority has invalid lifecycle", nil)
	}
	cleanupCtx := processCleanupContext(ctx)
	result := error(nil)
	if state.relay != nil && state.relay.gate != nil {
		result = appendBlockedLaunchProcessCleanupError(
			result,
			"close blocked relay gate",
			state.relay.gate.Release(cleanupCtx),
			state.vmID,
		)
	}
	if state.firecracker.gate != nil {
		result = appendBlockedLaunchProcessCleanupError(
			result,
			"close blocked Firecracker gate",
			state.firecracker.gate.Release(cleanupCtx),
			state.vmID,
		)
	}
	if state.receiverExecutable != nil {
		result = appendBlockedLaunchProcessCleanupError(
			result,
			"release pinned receiver executable",
			state.deps.releasePinnedExecutable(cleanupCtx, state.receiverExecutable),
			state.vmID,
		)
	}
	if state.relay != nil && state.relay.pidfd >= 0 {
		relayPIDFD := state.relay.pidfd
		result = appendBlockedLaunchProcessCleanupError(
			result,
			"close blocked relay pidfd",
			state.deps.close(cleanupCtx, relayPIDFD),
			state.vmID,
		)
		if state.firecracker.pidfd == relayPIDFD {
			state.firecracker.pidfd = -1
		}
	}
	if state.firecracker.pidfd >= 0 {
		result = appendBlockedLaunchProcessCleanupError(
			result,
			"close blocked Firecracker pidfd",
			state.deps.close(cleanupCtx, state.firecracker.pidfd),
			state.vmID,
		)
	}
	state.firecracker = blockedLaunchProcess{pidfd: -1}
	state.relay = nil
	state.receiverExecutable = nil
	state.status = blockedLaunchProcessesReleased
	state.releaseErr = result
	return state.releaseErr
}

func processCleanupContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func appendBlockedLaunchProcessCleanupError(primary error, message string, cause error, vmID string) error {
	if cause == nil {
		return primary
	}
	joined := cause
	if primary != nil {
		joined = errors.Join(primary, cause)
	}
	source := errs.AsDomainError(primary)
	if source == nil {
		source = errs.AsDomainError(cause)
	}
	if source == nil {
		result := processAuthorityError(vmID, message, joined)
		result.Details = map[string]any{
			"blocked_launch_process_cleanup_failed": true,
			"blocked_launch_process_cleanup_detail": boundedInstanceCleanupDetail(message, cause),
		}
		return result
	}
	cloned := *source
	cloned.Details = cloneInstanceErrorDetails(source.Details)
	cloned.Err = joined
	if cloned.Details == nil {
		cloned.Details = make(map[string]any, 2)
	}
	cloned.Details["blocked_launch_process_cleanup_failed"] = true
	if _, exists := cloned.Details["blocked_launch_process_cleanup_detail"]; !exists {
		cloned.Details["blocked_launch_process_cleanup_detail"] = boundedInstanceCleanupDetail(message, cause)
	}
	return &cloned
}

func processAuthorityError(vmID, message string, cause error) *errs.DomainError {
	options := []errs.ErrorOption{errs.WithClass(errs.ClassInternal)}
	if vmID != "" {
		options = append(options, errs.WithEntity(vmID))
	}
	if cause == nil {
		return errs.New(errs.CodeProcessError, message, options...)
	}
	return errs.WrapMsg(errs.CodeProcessError, message+": "+cause.Error(), cause, options...)
}
