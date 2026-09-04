package jailer

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	vmExecRelayStopPollSliceMilliseconds = 50
	vmExecRelayStopMaxTransientRetries   = 16
)

type vmExecRelayStopDeps struct {
	create func(context.Context, int, int) (int, error)
	arm    func(context.Context, int, int, *unix.ItimerSpec) error
	poll   func(context.Context, []unix.PollFd, int) (int, error)
	read   func(context.Context, int, []byte) (int, error)
	close  func(context.Context, int) error
}

// vmExecRelayStopFactory is the private action-specific constructor for the retained VM-exec relay deadline. It has
// no production caller until the exact jailer-vm-exec-session action composes the accepted authority graph.
type vmExecRelayStopFactory struct {
	deps vmExecRelayStopDeps
}

type vmExecRelayStop struct {
	state *vmExecRelayStopState
}

type vmExecRelayStopState struct {
	// mu guards the timer descriptor and the single-waiter, cached-result, and close-once lifecycle below. fdMu pins
	// the descriptor across one syscall without preventing another lifecycle call from observing the waiting state.
	mu         sync.Mutex
	fdMu       sync.RWMutex
	deps       vmExecRelayStopDeps
	fd         int
	waiting    bool
	waited     bool
	waitErr    error
	released   bool
	releaseErr error
}

func newVMExecRelayStopFactory() (*vmExecRelayStopFactory, error) {
	return newVMExecRelayStopFactoryWithDeps(realVMExecRelayStopDeps())
}

func newVMExecRelayStopFactoryWithDeps(deps vmExecRelayStopDeps) (*vmExecRelayStopFactory, error) {
	if deps.create == nil || deps.arm == nil || deps.poll == nil || deps.read == nil || deps.close == nil {
		return nil, vmExecRelayStopError("vm-exec relay-stop dependencies are incomplete", nil)
	}
	return &vmExecRelayStopFactory{deps: deps}, nil
}

// arm creates a one-shot CLOCK_BOOTTIME timer at the already-admitted absolute nanosecond deadline. It intentionally
// does not translate the authority's clock value through time.Time or extend a budget consumed by setup or suspend.
func (factory *vmExecRelayStopFactory) arm(
	ctx context.Context,
	relayStopBoottimeNS uint64,
) (*vmExecRelayStop, error) {
	if factory == nil {
		return nil, vmExecRelayStopError("vm-exec relay-stop factory is unavailable", nil)
	}
	if ctx == nil {
		return nil, vmExecRelayStopError("vm-exec relay-stop context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, vmExecRelayStopError("arm vm-exec relay-stop deadline", err)
	}
	value, err := vmExecRelayStopTimespec(relayStopBoottimeNS)
	if err != nil {
		return nil, err
	}
	fd, err := factory.deps.create(ctx, unix.CLOCK_BOOTTIME, unix.TFD_CLOEXEC|unix.TFD_NONBLOCK)
	if err != nil {
		return nil, vmExecRelayStopError("create CLOCK_BOOTTIME vm-exec relay-stop timer", err)
	}
	if fd < 0 {
		return nil, vmExecRelayStopError("CLOCK_BOOTTIME vm-exec relay-stop timer returned an invalid descriptor", nil)
	}
	if err := factory.deps.arm(ctx, fd, unix.TFD_TIMER_ABSTIME, &unix.ItimerSpec{Value: value}); err != nil {
		primary := vmExecRelayStopError("arm absolute CLOCK_BOOTTIME vm-exec relay-stop timer", err)
		cleanupCtx := context.WithoutCancel(ctx)
		return nil, appendVMExecRelayStopCleanupError(
			primary,
			"close vm-exec relay-stop timer after arm failure",
			factory.deps.close(cleanupCtx, fd),
		)
	}
	return &vmExecRelayStop{state: &vmExecRelayStopState{deps: factory.deps, fd: fd}}, nil
}

func vmExecRelayStopTimespec(relayStopBoottimeNS uint64) (unix.Timespec, error) {
	if relayStopBoottimeNS == 0 {
		return unix.Timespec{}, vmExecRelayStopError("vm-exec relay-stop deadline must be positive", nil)
	}
	seconds := relayStopBoottimeNS / uint64(1_000_000_000)
	if seconds > uint64(math.MaxInt64) {
		return unix.Timespec{}, vmExecRelayStopError("vm-exec relay-stop deadline seconds overflow time_t", nil)
	}
	return unix.Timespec{
		Sec:  int64(seconds),
		Nsec: int64(relayStopBoottimeNS % uint64(1_000_000_000)),
	}, nil
}

// Wait consumes the one-shot timerfd expiration. Polling is deliberately sliced so context cancellation remains
// bounded without converting the CLOCK_BOOTTIME authority into a Go time source. Only one waiter owns the read.
func (stop *vmExecRelayStop) Wait(ctx context.Context) error {
	return stop.wait(ctx)
}

// startWait returns a closed server-owned handle after its zero-time timer observation establishes whether the result
// is immediate or pending. A pending result is produced asynchronously and remains joinable by the façade.
func (stop *vmExecRelayStop) startWait(ctx context.Context) vmExecSessionServerStopWait {
	if stop == nil || stop.state == nil {
		return newVMExecSessionServerStopWait(
			false,
			vmExecRelayStopError("vm-exec relay-stop capability is unavailable", nil),
		)
	}
	if ctx == nil {
		return newVMExecSessionServerStopWait(
			false,
			vmExecRelayStopError("vm-exec relay-stop wait context is required", nil),
		)
	}
	if err := ctx.Err(); err != nil {
		return newVMExecSessionServerStopWait(
			false,
			vmExecRelayStopError("wait for vm-exec relay-stop deadline", err),
		)
	}
	state := stop.state
	state.mu.Lock()
	if state.released {
		state.mu.Unlock()
		return newVMExecSessionServerStopWait(
			false,
			vmExecRelayStopError("vm-exec relay-stop timer was released before wait", nil),
		)
	}
	if state.waited {
		result := state.waitErr
		state.mu.Unlock()
		return newVMExecSessionServerStopWait(false, result)
	}
	if state.waiting {
		state.mu.Unlock()
		return newVMExecSessionServerStopWait(
			false,
			vmExecRelayStopError("vm-exec relay-stop timer already has a waiter", nil),
		)
	}
	state.waiting = true
	state.mu.Unlock()

	pending, immediateResult := state.observeImmediateExpiration(ctx)
	if !pending {
		state.completeWait(immediateResult)
		return newVMExecSessionServerStopWait(false, immediateResult)
	}
	results := make(chan error, 1)
	go func() {
		result := state.waitForExpirationLoop(ctx, true)
		state.completeWait(result)
		results <- result
		close(results)
	}()
	return vmExecSessionServerStopWait{pending: true, result: results}
}

func (stop *vmExecRelayStop) wait(ctx context.Context) error {
	if stop == nil || stop.state == nil {
		return vmExecRelayStopError("vm-exec relay-stop capability is unavailable", nil)
	}
	if ctx == nil {
		return vmExecRelayStopError("vm-exec relay-stop wait context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return vmExecRelayStopError("wait for vm-exec relay-stop deadline", err)
	}
	state := stop.state
	state.mu.Lock()
	if state.released {
		state.mu.Unlock()
		return vmExecRelayStopError("vm-exec relay-stop timer was released before wait", nil)
	}
	if state.waited {
		result := state.waitErr
		state.mu.Unlock()
		return result
	}
	if state.waiting {
		state.mu.Unlock()
		return vmExecRelayStopError("vm-exec relay-stop timer already has a waiter", nil)
	}
	state.waiting = true
	state.mu.Unlock()

	result := state.waitForExpiration(ctx)
	state.completeWait(result)
	return result
}

func (state *vmExecRelayStopState) completeWait(result error) {
	state.mu.Lock()
	state.waiting = false
	state.waited = true
	state.waitErr = result
	state.mu.Unlock()
}

func (state *vmExecRelayStopState) waitForExpiration(ctx context.Context) error {
	return state.waitForExpirationLoop(ctx, false)
}

func (state *vmExecRelayStopState) waitForExpirationLoop(ctx context.Context, observeCancellation bool) error {
	transientFailures := 0
	for {
		if err := ctx.Err(); err != nil {
			if observeCancellation {
				return state.finishCancelledWait(ctx, err)
			}
			return vmExecRelayStopError("wait for vm-exec relay-stop deadline", err)
		}
		deps, fd, unpin, err := state.beginOperation("vm-exec relay-stop timer closed while waiting")
		if err != nil {
			return err
		}
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		count, err := deps.poll(ctx, pollFDs, vmExecRelayStopPollSliceMilliseconds)
		unpin()
		if observeCancellation && ctx.Err() != nil && (err == nil || errors.Is(err, ctx.Err())) {
			return state.finishCancelledWait(ctx, ctx.Err())
		}
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			transientFailures++
			if transientFailures > vmExecRelayStopMaxTransientRetries {
				return vmExecRelayStopError("poll vm-exec relay-stop timer exceeded transient retry bound", err)
			}
			continue
		}
		if err != nil {
			return vmExecRelayStopError("poll vm-exec relay-stop timer", err)
		}
		transientFailures = 0
		if count == 0 {
			if pollFDs[0].Revents != 0 {
				return vmExecRelayStopError("vm-exec relay-stop poll returned flags without an event", nil)
			}
			continue
		}
		if count != 1 {
			return vmExecRelayStopError(
				fmt.Sprintf("vm-exec relay-stop poll returned unexpected event count %d", count),
				nil,
			)
		}
		if err := validateVMExecRelayStopPollEvents(pollFDs[0].Revents); err != nil {
			return err
		}
		expired, err := state.consumeExpiration(ctx)
		if err != nil {
			if observeCancellation && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				return state.finishCancelledWait(ctx, ctx.Err())
			}
			return err
		}
		if expired {
			return nil
		}
	}
}

func (state *vmExecRelayStopState) finishCancelledWait(ctx context.Context, cancellation error) error {
	pending, result := state.observeImmediateExpiration(context.WithoutCancel(ctx))
	if !pending {
		return result
	}
	return vmExecRelayStopError("wait for vm-exec relay-stop deadline", cancellation)
}

func (state *vmExecRelayStopState) observeImmediateExpiration(
	ctx context.Context,
) (bool, error) {
	transientFailures := 0
	for {
		deps, fd, unpin, err := state.beginOperation("vm-exec relay-stop timer closed while waiting")
		if err != nil {
			return false, err
		}
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		count, err := deps.poll(ctx, pollFDs, 0)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			unpin()
			transientFailures++
			if transientFailures > vmExecRelayStopMaxTransientRetries {
				return false, vmExecRelayStopError(
					"poll vm-exec relay-stop timer exceeded transient retry bound",
					err,
				)
			}
			continue
		}
		if err != nil {
			unpin()
			return false, vmExecRelayStopError("poll vm-exec relay-stop timer", err)
		}
		if count == 0 {
			if pollFDs[0].Revents != 0 {
				unpin()
				return false, vmExecRelayStopError("vm-exec relay-stop poll returned flags without an event", nil)
			}
			unpin()
			return true, nil
		}
		if count != 1 {
			unpin()
			return false, vmExecRelayStopError(
				fmt.Sprintf("vm-exec relay-stop poll returned unexpected event count %d", count),
				nil,
			)
		}
		if err := validateVMExecRelayStopPollEvents(pollFDs[0].Revents); err != nil {
			unpin()
			return false, err
		}
		unpin()
		expired, err := state.consumeExpiration(ctx)
		if err != nil {
			return false, err
		}
		if expired {
			return false, nil
		}
	}
}

func validateVMExecRelayStopPollEvents(events int16) error {
	if events&unix.POLLNVAL != 0 {
		return vmExecRelayStopError("vm-exec relay-stop timer closed while waiting", nil)
	}
	if events&unix.POLLERR != 0 {
		return vmExecRelayStopError("vm-exec relay-stop timer reported a poll error", nil)
	}
	if events == 0 {
		return vmExecRelayStopError("vm-exec relay-stop poll returned an event without flags", nil)
	}
	if events != unix.POLLIN {
		return vmExecRelayStopError(
			fmt.Sprintf("vm-exec relay-stop poll returned unexpected events %#x", events),
			nil,
		)
	}
	return nil
}

func (state *vmExecRelayStopState) consumeExpiration(ctx context.Context) (bool, error) {
	deps, fd, unpin, err := state.beginOperation("vm-exec relay-stop timer closed before expiration read")
	if err != nil {
		return false, err
	}
	defer unpin()
	var value [8]byte
	for attempt := 0; attempt <= vmExecRelayStopMaxTransientRetries; attempt++ {
		count, err := deps.read(ctx, fd, value[:])
		if errors.Is(err, unix.EINTR) {
			if contextErr := ctx.Err(); contextErr != nil {
				return false, vmExecRelayStopError("read vm-exec relay-stop expiration", contextErr)
			}
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			return false, nil
		}
		if err != nil {
			return false, vmExecRelayStopError("read vm-exec relay-stop expiration", err)
		}
		if count == 0 {
			return false, vmExecRelayStopError("vm-exec relay-stop expiration read returned EOF", nil)
		}
		if count != len(value) {
			return false, vmExecRelayStopError("vm-exec relay-stop expiration read was incomplete", nil)
		}
		if binary.NativeEndian.Uint64(value[:]) == 0 {
			return false, vmExecRelayStopError("vm-exec relay-stop expiration count is zero", nil)
		}
		return true, nil
	}
	return false, vmExecRelayStopError(
		"read vm-exec relay-stop expiration exceeded transient retry bound",
		unix.EINTR,
	)
}

func (state *vmExecRelayStopState) beginOperation(message string) (vmExecRelayStopDeps, int, func(), error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return vmExecRelayStopDeps{}, -1, nil, vmExecRelayStopError(message, nil)
	}
	state.fdMu.RLock()
	return state.deps, state.fd, state.fdMu.RUnlock, nil
}

// Release closes the timer exactly once across copied capability values. A failed Linux close is never retried because
// the descriptor's state is ambiguous and retrying could close an unrelated descriptor that reused the number.
func (stop *vmExecRelayStop) Release(ctx context.Context) error {
	if stop == nil || stop.state == nil {
		return nil
	}
	state := stop.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	state.fdMu.Lock()
	defer state.fdMu.Unlock()
	cleanupCtx := context.Background()
	if ctx == nil {
		state.releaseErr = vmExecRelayStopError("vm-exec relay-stop release context is required", nil)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	state.releaseErr = appendVMExecRelayStopCleanupError(
		state.releaseErr,
		"close vm-exec relay-stop timer",
		state.deps.close(cleanupCtx, state.fd),
	)
	state.fd = -1
	state.released = true
	return state.releaseErr
}

func appendVMExecRelayStopCleanupError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	joined := cause
	if primary != nil {
		joined = errors.Join(primary, cause)
	}
	source := errs.AsDomainError(primary)
	if source == nil {
		source = vmExecRelayStopError("vm-exec relay-stop cleanup failed", cause)
	}
	result := *source
	result.Details = cloneInstanceErrorDetails(source.Details)
	if result.Details == nil {
		result.Details = make(map[string]any, 2)
	}
	result.Details["vm_exec_relay_stop_cleanup_failed"] = true
	if _, exists := result.Details["vm_exec_relay_stop_cleanup_detail"]; !exists {
		result.Details["vm_exec_relay_stop_cleanup_detail"] = boundedInstanceCleanupDetail(message, cause)
	}
	result.Err = joined
	return &result
}

func vmExecRelayStopError(message string, cause error) *errs.DomainError {
	class := errs.ClassInternal
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		class = errs.ClassRetryable
	}
	if cause == nil {
		return errs.New(errs.CodeVMAtomicFailed, message, errs.WithClass(class))
	}
	return errs.WrapMsg(errs.CodeVMAtomicFailed, message, cause, errs.WithClass(class))
}

func realVMExecRelayStopDeps() vmExecRelayStopDeps {
	return vmExecRelayStopDeps{
		create: vmExecRelayStopTimerfdCreate,
		arm:    vmExecRelayStopTimerfdSettime,
		poll:   vmExecRelayStopPoll,
		read:   vmExecRelayStopRead,
		close:  vmExecRelayStopClose,
	}
}

func vmExecRelayStopTimerfdCreate(ctx context.Context, clockID int, flags int) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	return unix.TimerfdCreate(clockID, flags)
}

func vmExecRelayStopTimerfdSettime(
	ctx context.Context,
	fd int,
	flags int,
	value *unix.ItimerSpec,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.TimerfdSettime(fd, flags, value, nil)
}

func vmExecRelayStopPoll(ctx context.Context, pollFDs []unix.PollFd, timeoutMS int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return unix.Poll(pollFDs, timeoutMS)
}

func vmExecRelayStopRead(ctx context.Context, fd int, value []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return unix.Read(fd, value)
}

func vmExecRelayStopClose(_ context.Context, fd int) error {
	return unix.Close(fd)
}
