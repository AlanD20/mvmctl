package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

const (
	privilegedVMExecSessionSudoPath   = "/usr/bin/sudo"
	privilegedVMExecSessionReapPeriod = 5 * time.Second
)

// PrivilegedVMExecSessionTransport starts the one fixed retained VM-exec sidecar protocol.
// Its zero value is ready for use.
type PrivilegedVMExecSessionTransport struct{}

// OpenSource starts the fixed sidecar and transfers ownership of sourceDuplicate.
func (PrivilegedVMExecSessionTransport) OpenSource(
	ctx context.Context,
	request vmexecsessionwire.OpenSource,
	sourceDuplicate *os.File,
) (*PrivilegedVMExecSession, error) {
	return openPrivilegedVMExecSession(
		ctx,
		request,
		sourceDuplicate,
		realPrivilegedVMExecSessionDeps(),
		productionPrivilegedVMExecSessionPolicy(),
	)
}

// PrivilegedVMExecSession is one ordered retained-sidecar exchange.
// Copies share the same process, state, and close-once cleanup.
type PrivilegedVMExecSession struct {
	shared *privilegedVMExecSessionShared
}

type privilegedVMExecSessionState uint8

const (
	privilegedVMExecSessionSourceRetained privilegedVMExecSessionState = iota + 1
	privilegedVMExecSessionBinding
	privilegedVMExecSessionReady
	privilegedVMExecSessionReleasing
	privilegedVMExecSessionTerminal
	privilegedVMExecSessionCancelled
)

type privilegedVMExecSessionShared struct {
	mu sync.Mutex

	state       privilegedVMExecSessionState
	control     *os.File
	command     *exec.Cmd
	deps        privilegedVMExecSessionDeps
	policy      privilegedVMExecSessionPolicy
	waitResult  <-chan error
	stdout      *privilegedControlBoundedWriter
	stderr      *privilegedControlBoundedWriter
	cleanupOnce sync.Once
	cleanupErr  error
	interrupted bool

	releaseDone    chan struct{}
	releaseOutcome vmexecsessionwire.Outcome
	releaseErr     error
}

type privilegedVMExecSessionPolicy struct {
	maxDiagnosticBytes int
	reapTimeout        time.Duration
}

func productionPrivilegedVMExecSessionPolicy() privilegedVMExecSessionPolicy {
	return privilegedVMExecSessionPolicy{
		maxDiagnosticBytes: privilegedControlMaxDiagnosticBytes,
		reapTimeout:        privilegedVMExecSessionReapPeriod,
	}
}

type privilegedVMExecSessionDeps = privilegedControlDeps

func realPrivilegedVMExecSessionDeps() privilegedVMExecSessionDeps {
	return realPrivilegedControlDeps()
}

func openPrivilegedVMExecSession(
	ctx context.Context,
	request vmexecsessionwire.OpenSource,
	sourceDuplicate *os.File,
	deps privilegedVMExecSessionDeps,
	policy privilegedVMExecSessionPolicy,
) (*PrivilegedVMExecSession, error) {
	cleanupCtx := context.Background()
	if ctx == nil {
		closeErr := closePrivilegedVMExecSessionFile(cleanupCtx, deps, sourceDuplicate)
		return nil, privilegedVMExecSessionFailure(false, false, errors.Join(
			errs.New(errs.CodeValidationFailed, "retained VM-exec session context is required"),
			closeErr,
		))
	}
	if sourceDuplicate == nil {
		return nil, privilegedVMExecSessionFailure(false, false, errs.New(
			errs.CodeValidationFailed,
			"retained VM-exec source connection duplicate is required",
		))
	}
	closeSource := func(primary error) error {
		return privilegedVMExecSessionFailure(false, false, errors.Join(
			primary,
			closePrivilegedVMExecSessionFile(cleanupCtx, deps, sourceDuplicate),
		))
	}
	if err := ctx.Err(); err != nil {
		return nil, closeSource(err)
	}
	var frame bytes.Buffer
	if err := vmexecsessionwire.EncodeOpenSource(ctx, &frame, request); err != nil {
		return nil, closeSource(err)
	}

	parent, child, err := deps.socketPair(ctx)
	if err != nil || parent == nil || child == nil {
		if err == nil {
			err = errors.New("retained VM-exec session socket pair returned a nil endpoint")
		}
		return nil, privilegedVMExecSessionFailure(false, false, errors.Join(
			err,
			closePrivilegedVMExecSessionFile(cleanupCtx, deps, sourceDuplicate),
			closePrivilegedVMExecSessionFile(cleanupCtx, deps, child),
			closePrivilegedVMExecSessionFile(cleanupCtx, deps, parent),
		))
	}
	closeBeforeStart := func(primary error) error {
		return privilegedVMExecSessionFailure(false, false, errors.Join(
			primary,
			closePrivilegedVMExecSessionFile(cleanupCtx, deps, sourceDuplicate),
			closePrivilegedVMExecSessionFile(cleanupCtx, deps, child),
			closePrivilegedVMExecSessionFile(cleanupCtx, deps, parent),
		))
	}

	command := deps.command(
		privilegedVMExecSessionSudoPath,
		"-n",
		"--",
		infra.SystemBinaryPath,
		infra.PrivilegedProtocolMarker,
		vmexecsessionwire.Action,
	)
	if command == nil {
		return nil, closeBeforeStart(errors.New("retained VM-exec session command construction returned nil"))
	}
	stdout := newPrivilegedControlBoundedWriter(policy.maxDiagnosticBytes)
	stderr := newPrivilegedControlBoundedWriter(policy.maxDiagnosticBytes)
	command.Stdin = child
	command.Stdout = stdout
	command.Stderr = stderr
	command.Dir = "/"
	command.Env = privilegedControlEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := ctx.Err(); err != nil {
		return nil, closeBeforeStart(err)
	}
	if err := deps.start(ctx, command); err != nil {
		return nil, closeBeforeStart(err)
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- deps.wait(context.Background(), command)
	}()
	shared := &privilegedVMExecSessionShared{
		state:      privilegedVMExecSessionSourceRetained,
		control:    parent,
		command:    command,
		deps:       deps,
		policy:     policy,
		waitResult: waitResult,
		stdout:     stdout,
		stderr:     stderr,
	}
	session := &PrivilegedVMExecSession{shared: shared}
	if err := closePrivilegedVMExecSessionFile(cleanupCtx, deps, child); err != nil {
		_ = closePrivilegedVMExecSessionFile(cleanupCtx, deps, sourceDuplicate)
		cleanupErr := shared.cleanup(true, true)
		return nil, privilegedVMExecSessionFailure(true, true, errors.Join(err, cleanupErr))
	}

	finishCancellation := shared.watchCancellation(ctx)
	if err := sendPrivilegedVMExecSessionDuplicate(ctx, parent, frame.Bytes(), sourceDuplicate); err != nil {
		finishCancellation()
		cleanupErr := shared.cleanup(true, true)
		return nil, privilegedVMExecSessionFailure(true, true, errors.Join(err, cleanupErr))
	}
	result, err := vmexecsessionwire.DecodeSourceRetained(ctx, parent)
	finishCancellation()
	if err != nil {
		cleanupErr := shared.cleanup(true, true)
		return nil, privilegedVMExecSessionFailure(true, true, errors.Join(err, cleanupErr))
	}
	if shared.wasInterrupted() {
		cleanupErr := shared.cleanup(true, true)
		return nil, privilegedVMExecSessionFailure(
			true,
			true,
			errors.Join(errors.New("retained VM-exec source response followed local interruption"), cleanupErr),
		)
	}
	if operationErr := result.OperationError(); operationErr != nil {
		_ = shared.cleanup(false, true)
		return nil, operationErr
	}
	if !result.Retained() {
		cleanupErr := shared.cleanup(true, true)
		return nil, privilegedVMExecSessionFailure(
			true,
			true,
			errors.Join(errors.New("retained VM-exec source response did not retain source"), cleanupErr),
		)
	}
	return session, nil
}

// BindDestination transfers ownership of destinationDuplicate and advances the fixed exchange to ready.
func (session *PrivilegedVMExecSession) BindDestination(
	ctx context.Context,
	destinationDuplicate *os.File,
) error {
	if session == nil || session.shared == nil {
		return closePrivilegedVMExecSessionRejectedDuplicate(
			ctx,
			destinationDuplicate,
			errs.New(errs.CodeValidationFailed, "retained VM-exec session is required"),
		)
	}
	return session.shared.bindDestination(ctx, destinationDuplicate)
}

func (shared *privilegedVMExecSessionShared) bindDestination(
	ctx context.Context,
	destinationDuplicate *os.File,
) error {
	if ctx == nil {
		return shared.rejectDuplicate(
			destinationDuplicate,
			privilegedVMExecSessionFailure(true, false, errs.New(
				errs.CodeValidationFailed,
				"retained VM-exec session context is required",
			)),
		)
	}
	if destinationDuplicate == nil {
		return privilegedVMExecSessionFailure(true, false, errs.New(
			errs.CodeValidationFailed,
			"retained VM-exec destination connection duplicate is required",
		))
	}
	if err := ctx.Err(); err != nil {
		return shared.rejectDuplicate(destinationDuplicate, privilegedVMExecSessionFailure(true, false, err))
	}

	shared.mu.Lock()
	if shared.state != privilegedVMExecSessionSourceRetained {
		shared.mu.Unlock()
		return shared.rejectDuplicate(destinationDuplicate, privilegedVMExecSessionStateFailure())
	}
	shared.state = privilegedVMExecSessionBinding
	shared.mu.Unlock()

	var frame bytes.Buffer
	if err := vmexecsessionwire.EncodeBindDestination(ctx, &frame); err != nil {
		closeErr := shared.rejectDuplicate(destinationDuplicate, nil)
		cleanupErr := shared.cleanup(true, true)
		return privilegedVMExecSessionFailure(true, true, errors.Join(err, closeErr, cleanupErr))
	}
	finishCancellation := shared.watchCancellation(ctx)
	if err := sendPrivilegedVMExecSessionDuplicate(
		ctx,
		shared.control,
		frame.Bytes(),
		destinationDuplicate,
	); err != nil {
		finishCancellation()
		cleanupErr := shared.cleanup(true, true)
		return privilegedVMExecSessionFailure(true, true, errors.Join(err, cleanupErr))
	}
	result, err := vmexecsessionwire.DecodeReady(ctx, shared.control)
	finishCancellation()
	if err != nil {
		cleanupErr := shared.cleanup(true, true)
		return privilegedVMExecSessionFailure(true, true, errors.Join(err, cleanupErr))
	}
	if shared.wasInterrupted() {
		cleanupErr := shared.cleanup(true, true)
		return privilegedVMExecSessionFailure(
			true,
			true,
			errors.Join(errors.New("retained VM-exec ready response followed local interruption"), cleanupErr),
		)
	}
	if operationErr := result.OperationError(); operationErr != nil {
		_ = shared.cleanup(false, true)
		return operationErr
	}
	if !result.Ready() {
		cleanupErr := shared.cleanup(true, true)
		return privilegedVMExecSessionFailure(
			true,
			true,
			errors.Join(errors.New("retained VM-exec response did not become ready"), cleanupErr),
		)
	}
	shared.mu.Lock()
	if shared.state != privilegedVMExecSessionBinding {
		shared.mu.Unlock()
		return privilegedVMExecSessionFailure(true, true, errors.New("retained VM-exec session was interrupted"))
	}
	shared.state = privilegedVMExecSessionReady
	shared.mu.Unlock()
	return nil
}

// Release requests the sole terminal release and returns its trusted outcome.
func (session *PrivilegedVMExecSession) Release(ctx context.Context) (vmexecsessionwire.Outcome, error) {
	if session == nil || session.shared == nil {
		return vmexecsessionwire.Outcome{}, privilegedVMExecSessionFailure(
			false,
			false,
			errs.New(errs.CodeValidationFailed, "retained VM-exec session is required"),
		)
	}
	return session.shared.release(ctx)
}

func (shared *privilegedVMExecSessionShared) release(
	ctx context.Context,
) (vmexecsessionwire.Outcome, error) {
	if ctx == nil {
		return vmexecsessionwire.Outcome{}, privilegedVMExecSessionFailure(
			true,
			false,
			errs.New(errs.CodeValidationFailed, "retained VM-exec session context is required"),
		)
	}
	if err := ctx.Err(); err != nil {
		return vmexecsessionwire.Outcome{}, privilegedVMExecSessionFailure(true, false, err)
	}

	shared.mu.Lock()
	switch shared.state {
	case privilegedVMExecSessionReady:
		shared.state = privilegedVMExecSessionReleasing
		shared.releaseDone = make(chan struct{})
		done := shared.releaseDone
		shared.mu.Unlock()
		outcome, err := shared.runRelease(ctx)
		shared.mu.Lock()
		shared.releaseOutcome = outcome
		shared.releaseErr = err
		shared.state = privilegedVMExecSessionTerminal
		close(done)
		shared.mu.Unlock()
		return outcome, err
	case privilegedVMExecSessionReleasing:
		done := shared.releaseDone
		shared.mu.Unlock()
		select {
		case <-done:
			shared.mu.Lock()
			outcome, err := shared.releaseOutcome, shared.releaseErr
			shared.mu.Unlock()
			return outcome, err
		case <-ctx.Done():
			return vmexecsessionwire.Outcome{}, privilegedVMExecSessionFailure(true, true, ctx.Err())
		}
	default:
		shared.mu.Unlock()
		return vmexecsessionwire.Outcome{}, privilegedVMExecSessionStateFailure()
	}
}

func (shared *privilegedVMExecSessionShared) runRelease(
	ctx context.Context,
) (vmexecsessionwire.Outcome, error) {
	finishCancellation := shared.watchCancellation(ctx)
	if err := vmexecsessionwire.EncodeRelease(ctx, shared.control); err != nil {
		finishCancellation()
		cleanupErr := shared.cleanup(true, true)
		return vmexecsessionwire.Outcome{}, privilegedVMExecSessionFailure(true, true, errors.Join(err, cleanupErr))
	}
	if err := shared.deps.shutdown(context.Background(), shared.control, unix.SHUT_WR); err != nil &&
		!errors.Is(err, unix.ENOTCONN) {
		finishCancellation()
		cleanupErr := shared.cleanup(true, true)
		return vmexecsessionwire.Outcome{}, privilegedVMExecSessionFailure(true, true, errors.Join(err, cleanupErr))
	}
	outcome, err := vmexecsessionwire.DecodeOutcome(ctx, shared.control)
	finishCancellation()
	if err != nil {
		cleanupErr := shared.cleanup(true, true)
		return vmexecsessionwire.Outcome{}, privilegedVMExecSessionFailure(true, true, errors.Join(err, cleanupErr))
	}
	if shared.wasInterrupted() {
		cleanupErr := shared.cleanup(true, true)
		return vmexecsessionwire.Outcome{}, privilegedVMExecSessionFailure(
			true,
			true,
			errors.Join(errors.New("retained VM-exec terminal response followed local interruption"), cleanupErr),
		)
	}
	cleanupErr := shared.cleanup(false, outcome.OperationError() != nil)
	if cleanupErr != nil {
		return outcome, privilegedVMExecSessionFailure(true, false, cleanupErr)
	}
	return outcome, nil
}

func (shared *privilegedVMExecSessionShared) watchCancellation(ctx context.Context) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = shared.cleanup(true, true)
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

// Cancel interrupts the fixed sidecar process and closes all transport-owned resources.
func (session *PrivilegedVMExecSession) Cancel(ctx context.Context) error {
	if session == nil || session.shared == nil {
		return privilegedVMExecSessionFailure(
			false,
			false,
			errs.New(errs.CodeValidationFailed, "retained VM-exec session is required"),
		)
	}
	if ctx == nil {
		return privilegedVMExecSessionFailure(
			true,
			false,
			errs.New(errs.CodeValidationFailed, "retained VM-exec session context is required"),
		)
	}
	shared := session.shared
	shared.mu.Lock()
	if shared.state != privilegedVMExecSessionTerminal {
		shared.state = privilegedVMExecSessionCancelled
	}
	shared.mu.Unlock()
	if err := shared.cleanup(true, true); err != nil {
		return privilegedVMExecSessionFailure(true, true, err)
	}
	return nil
}

func (shared *privilegedVMExecSessionShared) cleanup(kill bool, ignoreWaitError bool) error {
	if kill {
		shared.mu.Lock()
		shared.interrupted = true
		shared.mu.Unlock()
	}
	shared.cleanupOnce.Do(func() {
		cleanupCtx := context.Background()
		var cleanupErrors []error
		if err := shared.deps.shutdown(cleanupCtx, shared.control, unix.SHUT_RDWR); err != nil &&
			!errors.Is(err, unix.ENOTCONN) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("shutdown retained VM-exec control socket: %w", err))
		}
		if kill {
			if err := shared.killGroup(cleanupCtx); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		waitErr, timedOut := waitPrivilegedVMExecSession(shared.waitResult, shared.policy.reapTimeout)
		if timedOut && !kill {
			if err := shared.killGroup(cleanupCtx); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
			waitErr, timedOut = waitPrivilegedVMExecSession(shared.waitResult, shared.policy.reapTimeout)
		}
		if timedOut {
			cleanupErrors = append(
				cleanupErrors,
				errors.New("retained VM-exec sidecar did not exit before reap deadline"),
			)
		} else if waitErr != nil && !ignoreWaitError {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("wait for retained VM-exec sidecar: %w", waitErr))
		}
		if err := closePrivilegedVMExecSessionFile(cleanupCtx, shared.deps, shared.control); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("close retained VM-exec control socket: %w", err))
		}
		shared.cleanupErr = errors.Join(cleanupErrors...)
	})
	return shared.cleanupErr
}

func (shared *privilegedVMExecSessionShared) wasInterrupted() bool {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	return shared.interrupted
}

func (shared *privilegedVMExecSessionShared) killGroup(ctx context.Context) error {
	if shared.command == nil || shared.command.Process == nil || shared.command.Process.Pid <= 1 {
		return nil
	}
	if err := shared.deps.killGroup(ctx, shared.command.Process.Pid); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("kill retained VM-exec sidecar process group: %w", err)
	}
	return nil
}

func waitPrivilegedVMExecSession(waitResult <-chan error, timeout time.Duration) (error, bool) {
	if timeout <= 0 {
		return errors.New("retained VM-exec reap timeout must be positive"), true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waitResult:
		return err, false
	case <-timer.C:
		return nil, true
	}
}

func sendPrivilegedVMExecSessionDuplicate(
	ctx context.Context,
	control *os.File,
	frame []byte,
	duplicate *os.File,
) error {
	sender, err := newPrivilegedControlVMExecConnectionSender(duplicate)
	if err != nil {
		return errors.Join(err, duplicate.Close())
	}
	return sender.send(ctx, control, frame)
}

func (shared *privilegedVMExecSessionShared) rejectDuplicate(duplicate *os.File, primary error) error {
	closeErr := closePrivilegedVMExecSessionFile(context.Background(), shared.deps, duplicate)
	if primary == nil {
		return closeErr
	}
	return errors.Join(primary, closeErr)
}

func closePrivilegedVMExecSessionRejectedDuplicate(
	ctx context.Context,
	duplicate *os.File,
	primary error,
) error {
	_ = ctx
	if duplicate == nil {
		return privilegedVMExecSessionFailure(false, false, primary)
	}
	return privilegedVMExecSessionFailure(false, false, errors.Join(primary, duplicate.Close()))
}

func closePrivilegedVMExecSessionFile(
	ctx context.Context,
	deps privilegedVMExecSessionDeps,
	file *os.File,
) error {
	if file == nil {
		return nil
	}
	return deps.closeFile(ctx, file)
}

func privilegedVMExecSessionStateFailure() error {
	return privilegedVMExecSessionFailure(true, false, errs.New(
		errs.CodeValidationFailed,
		"retained VM-exec session method is repeated or out of order",
	))
}

func privilegedVMExecSessionFailure(started, outcomeUnknown bool, cause error) error {
	return errs.WrapMsg(
		errs.CodeProcessError,
		"retained VM-exec session transport failed",
		cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{
			"process_started": started,
			"outcome_unknown": outcomeUnknown,
		}),
	)
}
