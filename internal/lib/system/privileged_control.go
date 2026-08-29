package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	privilegedControlSudoPath             = "/usr/bin/sudo"
	privilegedControlMaxActionBytes       = 64
	privilegedControlMaxRequestFrameBytes = 8 + 4 + 8 + 64*1024
	privilegedControlMaxPayloadBytes      = 128 * 1024 * 1024
	privilegedControlMaxResponseBytes     = 8 + 4 + 64*1024
	privilegedControlMaxDiagnosticBytes   = 8 * 1024
	privilegedControlDefaultReapTimeout   = 5 * time.Second
	privilegedControlDefaultDrainTimeout  = time.Second
)

func privilegedControlEnvironment() []string {
	return []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/",
		"LANG=C",
		"LC_ALL=C",
	}
}

// PrivilegedControlRequest is a single-use, bounded request for the fixed
// privileged control transport. Successful construction transfers payload ownership.
type PrivilegedControlRequest struct {
	// mu guards the single-use consumed state.
	mu            sync.Mutex
	action        string
	frame         []byte
	payloadLength uint64
	payload       *privilegedControlPayloadOwner
	consumed      bool
}

// NewPrivilegedControlRequest validates one already encoded request. Payload
// ownership transfers only when construction succeeds; rejected inputs retain it.
// A supplied payload's Close method must unblock a concurrent Read because the
// transport closes it after a final peer response or cancellation.
// Capability clients remain responsible for producing the typed wire frame.
func NewPrivilegedControlRequest(
	action string,
	frame []byte,
	payloadLength uint64,
	payload io.ReadCloser,
) (*PrivilegedControlRequest, error) {
	if !validPrivilegedControlAction(action) {
		return nil, errs.New(errs.CodeValidationFailed, "privileged action must be a 1-64 byte ASCII identifier")
	}
	if len(frame) == 0 || len(frame) > privilegedControlMaxRequestFrameBytes {
		return nil, errs.New(errs.CodeValidationFailed, "privileged request frame has an invalid length")
	}
	if payloadLength > privilegedControlMaxPayloadBytes {
		return nil, errs.New(errs.CodeValidationFailed, "privileged request payload exceeds transport limit")
	}
	if payloadLength > 0 && payload == nil {
		return nil, errs.New(errs.CodeValidationFailed, "privileged request payload is missing")
	}

	var owner *privilegedControlPayloadOwner
	if payload != nil {
		owner = &privilegedControlPayloadOwner{payload: payload}
	}
	return &PrivilegedControlRequest{
		action:        action,
		frame:         append([]byte(nil), frame...),
		payloadLength: payloadLength,
		payload:       owner,
	}, nil
}

func (request *PrivilegedControlRequest) consume() (
	string,
	[]byte,
	uint64,
	*privilegedControlPayloadOwner,
	error,
) {
	if request == nil {
		return "", nil, 0, nil, errs.New(errs.CodeValidationFailed, "privileged control request is required")
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.consumed {
		return "", nil, 0, nil, errs.New(errs.CodeValidationFailed, "privileged control request was already used")
	}
	request.consumed = true
	return request.action, request.frame, request.payloadLength, request.payload, nil
}

func validPrivilegedControlAction(action string) bool {
	if len(action) == 0 || len(action) > privilegedControlMaxActionBytes {
		return false
	}
	for index := range len(action) {
		value := action[index]
		if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' {
			continue
		}
		if value != '.' && value != '_' && value != '-' {
			return false
		}
	}
	return true
}

type privilegedControlPayloadOwner struct {
	once    sync.Once
	payload io.ReadCloser
	err     error
}

func (owner *privilegedControlPayloadOwner) Read(target []byte) (int, error) {
	if owner == nil || owner.payload == nil {
		return 0, io.EOF
	}
	return owner.payload.Read(target)
}

func (owner *privilegedControlPayloadOwner) Close() error {
	if owner == nil {
		return nil
	}
	owner.once.Do(func() {
		owner.err = owner.payload.Close()
	})
	return owner.err
}

// PrivilegedControlTransport executes the one fixed privileged control command.
// Its zero value is ready for use.
type PrivilegedControlTransport struct{}

// Exchange transfers one request and returns transport evidence. A response is
// authoritative only after a capability client decodes it and ResponseEOF is true.
// CRITICAL: The executable, marker, environment, working directory, and argv shape
// are fixed here; callers control only the bounded action and encoded typed frame.
func (PrivilegedControlTransport) Exchange(
	ctx context.Context,
	request *PrivilegedControlRequest,
) PrivilegedControlOutcome {
	return exchangePrivilegedControl(ctx, request, realPrivilegedControlDeps(), productionPrivilegedControlPolicy())
}

// PrivilegedControlOutcome separates raw response evidence from transport diagnostics.
type PrivilegedControlOutcome struct {
	response         []byte
	responseEOF      bool
	processStarted   bool
	diagnostic       *errs.DomainError
	diagnosticStdout []byte
	diagnosticStderr []byte
	stdoutTruncated  bool
	stderrTruncated  bool
}

// Response returns a defensive copy of the bytes read from fd 0.
func (outcome PrivilegedControlOutcome) Response() []byte {
	return append([]byte(nil), outcome.response...)
}

// ResponseEOF reports whether the peer produced actual EOF before local interruption.
func (outcome PrivilegedControlOutcome) ResponseEOF() bool { return outcome.responseEOF }

// ProcessStarted reports whether sudo was started successfully.
func (outcome PrivilegedControlOutcome) ProcessStarted() bool { return outcome.processStarted }

// Diagnostic reports transport/process failure only; it is never response authority.
func (outcome PrivilegedControlOutcome) Diagnostic() *errs.DomainError { return outcome.diagnostic }

// DiagnosticStdout returns bounded stdout bytes which are never response authority.
func (outcome PrivilegedControlOutcome) DiagnosticStdout() []byte {
	return append([]byte(nil), outcome.diagnosticStdout...)
}

// DiagnosticStderr returns bounded stderr bytes which are never response authority.
func (outcome PrivilegedControlOutcome) DiagnosticStderr() []byte {
	return append([]byte(nil), outcome.diagnosticStderr...)
}

// DiagnosticStdoutTruncated reports whether stdout exceeded its diagnostic bound.
func (outcome PrivilegedControlOutcome) DiagnosticStdoutTruncated() bool {
	return outcome.stdoutTruncated
}

// DiagnosticStderrTruncated reports whether stderr exceeded its diagnostic bound.
func (outcome PrivilegedControlOutcome) DiagnosticStderrTruncated() bool {
	return outcome.stderrTruncated
}

type privilegedControlPolicy struct {
	maxResponseBytes         int
	maxDiagnosticBytes       int
	postResponseReapTimeout  time.Duration
	postInterruptReapTimeout time.Duration
	postExitDrainTimeout     time.Duration
}

func productionPrivilegedControlPolicy() privilegedControlPolicy {
	return privilegedControlPolicy{
		maxResponseBytes:         privilegedControlMaxResponseBytes,
		maxDiagnosticBytes:       privilegedControlMaxDiagnosticBytes,
		postResponseReapTimeout:  privilegedControlDefaultReapTimeout,
		postInterruptReapTimeout: privilegedControlDefaultReapTimeout,
		postExitDrainTimeout:     privilegedControlDefaultDrainTimeout,
	}
}

type privilegedControlDeps struct {
	socketPair func(ctx context.Context) (*os.File, *os.File, error)
	command    func(path string, args ...string) *exec.Cmd
	start      func(ctx context.Context, command *exec.Cmd) error
	wait       func(ctx context.Context, command *exec.Cmd) error
	shutdown   func(ctx context.Context, file *os.File, how int) error
	killGroup  func(ctx context.Context, pid int) error
	closeFile  func(ctx context.Context, file *os.File) error
}

func realPrivilegedControlDeps() privilegedControlDeps {
	return privilegedControlDeps{
		socketPair: openPrivilegedControlSocketPair,
		command:    exec.Command,
		start: func(_ context.Context, command *exec.Cmd) error {
			return command.Start()
		},
		wait: func(_ context.Context, command *exec.Cmd) error {
			return command.Wait()
		},
		shutdown: func(_ context.Context, file *os.File, how int) error {
			return unix.Shutdown(int(file.Fd()), how)
		},
		killGroup: func(_ context.Context, pid int) error {
			return unix.Kill(-pid, unix.SIGKILL)
		},
		closeFile: func(_ context.Context, file *os.File) error {
			return file.Close()
		},
	}
}

func openPrivilegedControlSocketPair(ctx context.Context) (*os.File, *os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "mvm-privileged-control-parent"),
		os.NewFile(uintptr(fds[1]), "mvm-privileged-control-child"), nil
}

type privilegedControlUploadResult struct{ err error }

type privilegedControlResponseResult struct {
	response []byte
	eof      bool
	err      error
}

func exchangePrivilegedControl(
	ctx context.Context,
	request *PrivilegedControlRequest,
	deps privilegedControlDeps,
	policy privilegedControlPolicy,
) PrivilegedControlOutcome {
	if ctx == nil {
		return PrivilegedControlOutcome{diagnostic: errs.New(
			errs.CodeValidationFailed,
			"privileged control context is required",
			errs.WithDetails(map[string]any{
				"process_started": false,
				"outcome_unknown": false,
			}),
		)}
	}
	action, frame, payloadLength, payload, err := request.consume()
	if err != nil {
		return privilegedControlFailure(false, false, err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if err := ctx.Err(); err != nil {
		closeErr := payload.Close()
		return privilegedControlFailure(false, false, errors.Join(err, closeErr))
	}

	parent, child, err := deps.socketPair(ctx)
	if err != nil || parent == nil || child == nil {
		if err == nil {
			err = errors.New("privileged control socket pair returned a nil endpoint")
		}
		closeErr := payload.Close()
		return privilegedControlFailure(false, false, errors.Join(
			err,
			closeErr,
			closePrivilegedControlFile(cleanupCtx, deps, child),
			closePrivilegedControlFile(cleanupCtx, deps, parent),
		))
	}
	closeBeforeStart := func(primary error) PrivilegedControlOutcome {
		return privilegedControlFailure(false, false, errors.Join(
			primary,
			payload.Close(),
			deps.closeFile(cleanupCtx, child),
			deps.closeFile(cleanupCtx, parent),
		))
	}

	command := deps.command(
		privilegedControlSudoPath,
		"-n",
		"--",
		infra.SystemBinaryPath,
		infra.PrivilegedProtocolMarker,
		action,
	)
	if command == nil {
		return closeBeforeStart(errors.New("privileged control command construction returned nil"))
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
		return closeBeforeStart(err)
	}
	if err := deps.start(ctx, command); err != nil {
		return closeBeforeStart(err)
	}

	outcome := PrivilegedControlOutcome{processStarted: true}
	var transportErrors []error
	if err := deps.closeFile(cleanupCtx, child); err != nil {
		transportErrors = append(transportErrors, fmt.Errorf("close child control socket: %w", err))
	}

	uploads := make(chan privilegedControlUploadResult, 1)
	responses := make(chan privilegedControlResponseResult, 1)
	waits := make(chan error, 1)
	go func() {
		uploadErr := uploadPrivilegedControl(ctx, parent, frame, payloadLength, payload)
		shutdownErr := deps.shutdown(cleanupCtx, parent, unix.SHUT_WR)
		if shutdownErr != nil && !errors.Is(shutdownErr, unix.ENOTCONN) {
			uploadErr = errors.Join(uploadErr, fmt.Errorf("half-close privileged request: %w", shutdownErr))
		}
		uploads <- privilegedControlUploadResult{err: uploadErr}
	}()
	go func() {
		responses <- readPrivilegedControlResponse(ctx, parent, policy.maxResponseBytes)
	}()
	go func() {
		waits <- deps.wait(cleanupCtx, command)
	}()

	var uploadDone, responseDone, waitDone bool
	var locallyInterrupted bool
	ctxDone := ctx.Done()
	var reapTimer, interruptReapTimer, drainTimer *time.Timer
	var reapTimerC, interruptReapTimerC, drainTimerC <-chan time.Time

	interrupt := func(kill bool) {
		if locallyInterrupted {
			return
		}
		locallyInterrupted = true
		// Close is idempotent; final cleanup retrieves and records its stored error.
		_ = payload.Close()
		if shutdownErr := deps.shutdown(cleanupCtx, parent, unix.SHUT_RDWR); shutdownErr != nil &&
			!errors.Is(shutdownErr, unix.ENOTCONN) {
			transportErrors = append(
				transportErrors,
				fmt.Errorf("interrupt privileged control socket: %w", shutdownErr),
			)
		}
		if kill && command.Process != nil && command.Process.Pid > 1 {
			if killErr := deps.killGroup(cleanupCtx, command.Process.Pid); killErr != nil &&
				!errors.Is(killErr, unix.ESRCH) {
				transportErrors = append(transportErrors, fmt.Errorf("kill privileged process group: %w", killErr))
			}
		}
		if !waitDone {
			// The caller may lose permission to signal sudo after it becomes root.
			// Bound reaping even when socket interruption and process-group kill fail.
			interruptReapTimer = time.NewTimer(policy.postInterruptReapTimeout)
			interruptReapTimerC = interruptReapTimer.C
		}
	}

	for !uploadDone || !responseDone || !waitDone {
		select {
		case upload := <-uploads:
			uploadDone = true
			if upload.err != nil {
				transportErrors = append(transportErrors, upload.err)
			}
		case response := <-responses:
			responseDone = true
			if drainTimer != nil {
				drainTimer.Stop()
				drainTimerC = nil
			}
			outcome.response = response.response
			outcome.responseEOF = response.eof && !locallyInterrupted
			if response.err != nil {
				transportErrors = append(transportErrors, response.err)
			}
			if response.eof {
				// Stop both possible upload blockers after an early final response:
				// payload Close releases a blocked Read and SHUT_WR releases a blocked
				// socket Write. Neither changes the peer-EOF evidence.
				_ = payload.Close()
				if shutdownErr := deps.shutdown(cleanupCtx, parent, unix.SHUT_WR); shutdownErr != nil &&
					!errors.Is(shutdownErr, unix.ENOTCONN) {
					transportErrors = append(
						transportErrors,
						fmt.Errorf("stop privileged upload after response EOF: %w", shutdownErr),
					)
				}
			}
			if !response.eof {
				interrupt(true)
			}
			if outcome.responseEOF && !waitDone && reapTimer == nil {
				reapTimer = time.NewTimer(policy.postResponseReapTimeout)
				reapTimerC = reapTimer.C
			}
		case waitErr := <-waits:
			waitDone = true
			if reapTimer != nil {
				reapTimer.Stop()
				reapTimerC = nil
			}
			if interruptReapTimer != nil {
				interruptReapTimer.Stop()
				interruptReapTimerC = nil
			}
			if waitErr != nil {
				transportErrors = append(transportErrors, fmt.Errorf("wait for privileged process: %w", waitErr))
			}
			if !responseDone && drainTimer == nil {
				drainTimer = time.NewTimer(policy.postExitDrainTimeout)
				drainTimerC = drainTimer.C
			}
		case <-ctxDone:
			ctxDone = nil
			// Prefer peer EOF already queued at the cancellation boundary.
			if !responseDone {
				if response, ok := takeQueuedPrivilegedControlResponse(responses); ok {
					responseDone = true
					if drainTimer != nil {
						drainTimer.Stop()
						drainTimerC = nil
					}
					outcome.response = response.response
					outcome.responseEOF = response.eof
					if response.err != nil {
						transportErrors = append(transportErrors, response.err)
					}
				}
			}
			transportErrors = append(transportErrors, ctx.Err())
			interrupt(true)
		case <-reapTimerC:
			reapTimerC = nil
			transportErrors = append(transportErrors, errors.New("privileged process did not exit after response EOF"))
			interrupt(true)
		case <-interruptReapTimerC:
			interruptReapTimerC = nil
			waitDone = true
			transportErrors = append(
				transportErrors,
				errors.New("privileged process did not exit after interruption"),
			)
		case <-drainTimerC:
			drainTimerC = nil
			transportErrors = append(
				transportErrors,
				errors.New("privileged response did not reach EOF after process exit"),
			)
			interrupt(true)
		}
	}

	if reapTimer != nil {
		reapTimer.Stop()
	}
	if interruptReapTimer != nil {
		interruptReapTimer.Stop()
	}
	if drainTimer != nil {
		drainTimer.Stop()
	}
	if closeErr := payload.Close(); closeErr != nil {
		transportErrors = append(transportErrors, fmt.Errorf("close privileged payload: %w", closeErr))
	}
	if closeErr := deps.closeFile(cleanupCtx, parent); closeErr != nil {
		transportErrors = append(transportErrors, fmt.Errorf("close parent control socket: %w", closeErr))
	}
	outcome.diagnosticStdout, outcome.stdoutTruncated = stdout.snapshot()
	outcome.diagnosticStderr, outcome.stderrTruncated = stderr.snapshot()
	if len(transportErrors) > 0 {
		outcome.diagnostic = newPrivilegedControlDiagnostic(true, true, errors.Join(transportErrors...))
	}
	return outcome
}

func takeQueuedPrivilegedControlResponse(
	responses <-chan privilegedControlResponseResult,
) (privilegedControlResponseResult, bool) {
	select {
	case response := <-responses:
		return response, true
	default:
		return privilegedControlResponseResult{}, false
	}
}

func closePrivilegedControlFile(
	ctx context.Context,
	deps privilegedControlDeps,
	file *os.File,
) error {
	if file == nil {
		return nil
	}
	return deps.closeFile(ctx, file)
}

func uploadPrivilegedControl(
	ctx context.Context,
	control io.Writer,
	frame []byte,
	payloadLength uint64,
	payload *privilegedControlPayloadOwner,
) error {
	// Final transport cleanup retrieves the idempotent owner's stored close error.
	defer func() { _ = payload.Close() }()
	if err := writePrivilegedControlExact(ctx, control, frame); err != nil {
		return fmt.Errorf("write privileged request frame: %w", err)
	}
	if payloadLength == 0 {
		return nil
	}
	written, err := io.CopyN(
		privilegedControlContextWriter{ctx: ctx, writer: control},
		privilegedControlContextReader{ctx: ctx, reader: payload},
		int64(payloadLength),
	)
	if err != nil {
		return fmt.Errorf("copy privileged request payload: %w", err)
	}
	if uint64(written) != payloadLength {
		return fmt.Errorf("copy privileged request payload: %w", io.ErrShortWrite)
	}
	return nil
}

type privilegedControlContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader privilegedControlContextReader) Read(target []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(target)
}

type privilegedControlContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer privilegedControlContextWriter) Write(value []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	return writer.writer.Write(value)
}

func writePrivilegedControlExact(ctx context.Context, writer io.Writer, value []byte) error {
	for len(value) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		written, err := writer.Write(value)
		if written > 0 {
			value = value[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readPrivilegedControlResponse(
	ctx context.Context,
	control io.Reader,
	maximum int,
) privilegedControlResponseResult {
	if err := ctx.Err(); err != nil {
		return privilegedControlResponseResult{err: err}
	}
	response, err := io.ReadAll(io.LimitReader(control, int64(maximum)+1))
	if err != nil {
		return privilegedControlResponseResult{response: response, err: err}
	}
	if len(response) > maximum {
		return privilegedControlResponseResult{
			response: response,
			err:      errors.New("privileged response exceeds transport limit"),
		}
	}
	return privilegedControlResponseResult{response: response, eof: true}
}

type privilegedControlBoundedWriter struct {
	// mu guards value and truncated while os/exec drains output concurrently.
	mu        sync.Mutex
	maximum   int
	value     []byte
	truncated bool
}

func newPrivilegedControlBoundedWriter(maximum int) *privilegedControlBoundedWriter {
	return &privilegedControlBoundedWriter{maximum: maximum}
}

func (writer *privilegedControlBoundedWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	remaining := writer.maximum - len(writer.value)
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		writer.value = append(writer.value, value[:remaining]...)
	}
	if remaining < len(value) {
		writer.truncated = true
	}
	return len(value), nil
}

func (writer *privilegedControlBoundedWriter) snapshot() ([]byte, bool) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return append([]byte(nil), writer.value...), writer.truncated
}

func privilegedControlFailure(started, outcomeUnknown bool, err error) PrivilegedControlOutcome {
	return PrivilegedControlOutcome{
		processStarted: started,
		diagnostic:     newPrivilegedControlDiagnostic(started, outcomeUnknown, err),
	}
}

func newPrivilegedControlDiagnostic(started, outcomeUnknown bool, err error) *errs.DomainError {
	if err == nil {
		return nil
	}
	return errs.WrapMsg(
		errs.CodeProcessError,
		"privileged control transport failed",
		err,
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{
			"process_started": started,
			"outcome_unknown": outcomeUnknown,
		}),
	)
}
