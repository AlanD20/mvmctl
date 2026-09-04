package privileged

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

const (
	vmExecSessionSetupReadTimeout   = 5 * time.Second
	vmExecSessionReleaseReadTimeout = 5 * time.Minute
)

type vmExecSessionHandlerEffects struct {
	openSource func(
		context.Context,
		callerIdentity,
		vmexecsessionwire.OpenSource,
		vmExecSessionConnection,
	) (vmExecSessionHandlerLease, error)
}

type vmExecSessionHandlerLease interface {
	bindDestination(context.Context, vmExecSessionConnection) error
	waitStop(context.Context) error
	release(context.Context) error
}

type vmExecSessionStopScope struct {
	ctx        context.Context
	parent     context.Context
	cancel     context.CancelCauseFunc
	done       <-chan struct{}
	finishOnce sync.Once
	// mu guards the stop-owned error published only when the waiter wins cancellation arbitration.
	mu      sync.Mutex
	stopErr *errs.DomainError
}

// vmExecSessionConnection is a borrowed view over a handler-owned retained connection.
// It intentionally exposes no descriptor, file, close operation, or generic callback.
type vmExecSessionConnection struct {
	retained *vmExecRetainedConnection
}

type vmExecSessionHandlerDeps struct {
	control          controlDeps
	connectionReader vmExecConnectionReaderDeps
}

type vmExecSessionResources struct {
	// mu guards exact-once cleanup and its cached result.
	mu          sync.Mutex
	source      *vmExecRetainedConnection
	destination *vmExecRetainedConnection
	lease       vmExecSessionHandlerLease
	cleaned     bool
	cleanupErr  error
}

// CRITICAL: This unregistered handler receives only the exact closed sidecar phases and borrowed connection
// capabilities. It accepts no action, path, command, token, policy material, raw descriptor, or payload.
func handleVMExecSession(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	effects vmExecSessionHandlerEffects,
	deps vmExecSessionHandlerDeps,
) error {
	if err := validateVMExecSessionHandlerInputs(ctx, endpoint, deps); err != nil {
		return err
	}
	resources := &vmExecSessionResources{}

	sourceDeadline := boundedReadDeadline(ctx, deps.control.now(), vmExecSessionSetupReadTimeout)
	sourceReader := newVMExecConnectionReaderWithDeps(
		ctx,
		int(endpoint.Fd()),
		sourceDeadline,
		deps.connectionReader,
	)
	request, source, sourceErr := sourceReader.decodeOpenSourceAndRetain(ctx)
	sourceCloseErr := sourceReader.Close()
	if sourceErr != nil {
		if errors.Is(sourceErr, io.EOF) {
			return sourceCloseErr
		}
		return failVMExecSession(ctx, endpoint, resources, errors.Join(sourceErr, sourceCloseErr), deps.control)
	}
	if sourceCloseErr != nil {
		return failVMExecSession(ctx, endpoint, resources, sourceCloseErr, deps.control)
	}
	resources.source = source

	if effects.openSource == nil {
		return failVMExecSession(
			ctx,
			endpoint,
			resources,
			errs.New(errs.CodeInternal, "retained VM-exec source handler is not active"),
			deps.control,
		)
	}
	lease, openErr := effects.openSource(
		ctx,
		caller,
		request,
		vmExecSessionConnection{retained: source},
	)
	resources.lease = lease
	if openErr != nil {
		return failVMExecSession(ctx, endpoint, resources, openErr, deps.control)
	}
	if lease == nil {
		return failVMExecSession(
			ctx,
			endpoint,
			resources,
			errs.New(errs.CodeInternal, "retained VM-exec source handler returned no lease"),
			deps.control,
		)
	}
	stopScope := startVMExecSessionStopWaiter(ctx, lease)
	defer stopScope.finish()
	if err := stopScope.finishIfInterrupted(); err != nil {
		return failVMExecSession(ctx, endpoint, resources, err, deps.control)
	}
	if err := deliverVMExecSourceRetained(stopScope.ctx, endpoint, deps.control); err != nil {
		return withVMExecSessionCleanupEvidence(stopScope.finishAndPrimary(err), resources.cleanup(ctx))
	}
	if err := stopScope.finishIfInterrupted(); err != nil {
		return failVMExecSession(ctx, endpoint, resources, err, deps.control)
	}

	destinationDeadline := boundedReadDeadline(stopScope.ctx, deps.control.now(), vmExecSessionSetupReadTimeout)
	destinationReader := newVMExecConnectionReaderWithDeps(
		stopScope.ctx,
		int(endpoint.Fd()),
		destinationDeadline,
		deps.connectionReader,
	)
	destination, destinationErr := destinationReader.decodeBindDestinationAndRetain(stopScope.ctx)
	destinationCloseErr := destinationReader.Close()
	if destinationErr != nil {
		if errors.Is(destinationErr, io.EOF) {
			return withVMExecSessionCleanupEvidence(
				stopScope.finishAndPrimary(destinationCloseErr),
				resources.cleanup(ctx),
			)
		}
		return failVMExecSession(
			ctx,
			endpoint,
			resources,
			stopScope.finishAndPrimary(errors.Join(destinationErr, destinationCloseErr)),
			deps.control,
		)
	}
	if destinationCloseErr != nil {
		return failVMExecSession(
			ctx,
			endpoint,
			resources,
			stopScope.finishAndPrimary(destinationCloseErr),
			deps.control,
		)
	}
	resources.destination = destination

	if err := lease.bindDestination(stopScope.ctx, vmExecSessionConnection{retained: destination}); err != nil {
		return failVMExecSession(ctx, endpoint, resources, stopScope.finishAndPrimary(err), deps.control)
	}
	if err := stopScope.finishIfInterrupted(); err != nil {
		return failVMExecSession(ctx, endpoint, resources, err, deps.control)
	}
	if err := deliverVMExecReady(stopScope.ctx, endpoint, deps.control); err != nil {
		return withVMExecSessionCleanupEvidence(stopScope.finishAndPrimary(err), resources.cleanup(ctx))
	}
	if err := stopScope.finishIfInterrupted(); err != nil {
		return failVMExecSession(ctx, endpoint, resources, err, deps.control)
	}

	releaseDeadline := boundedReadDeadline(stopScope.ctx, deps.control.now(), vmExecSessionReleaseReadTimeout)
	releaseReader := newVMExecReleaseFrameReaderWithDeps(
		stopScope.ctx,
		int(endpoint.Fd()),
		releaseDeadline,
		deps.connectionReader,
	)
	releaseErr := vmexecsessionwire.DecodeRelease(stopScope.ctx, releaseReader)
	if releaseErr == nil {
		releaseErr = requireVMExecSessionReleaseEOF(stopScope.ctx, releaseReader)
	}
	releaseCloseErr := releaseReader.Close()
	if releaseErr != nil {
		if errors.Is(releaseErr, io.EOF) {
			return withVMExecSessionCleanupEvidence(
				stopScope.finishAndPrimary(releaseCloseErr),
				resources.cleanup(ctx),
			)
		}
		return failVMExecSession(
			ctx,
			endpoint,
			resources,
			stopScope.finishAndPrimary(errors.Join(releaseErr, releaseCloseErr)),
			deps.control,
		)
	}
	if releaseCloseErr != nil {
		return failVMExecSession(
			ctx,
			endpoint,
			resources,
			stopScope.finishAndPrimary(releaseCloseErr),
			deps.control,
		)
	}
	if err := stopScope.finishAndPrimary(nil); err != nil {
		return failVMExecSession(ctx, endpoint, resources, err, deps.control)
	}

	cleanupErr := resources.cleanup(ctx)
	if cleanupErr != nil {
		return deliverVMExecFailure(
			ctx,
			endpoint,
			withVMExecSessionCleanupEvidence(nil, cleanupErr),
			deps.control,
		)
	}
	return deliverVMExecReleased(ctx, endpoint, deps.control)
}

// CRITICAL: The waiter only cancels protocol work. The handler remains the sole cleanup and response owner, and
// finish always cancels and joins the waiter before either responsibility can proceed.
func startVMExecSessionStopWaiter(
	ctx context.Context,
	lease vmExecSessionHandlerLease,
) *vmExecSessionStopScope {
	stopCtx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{}, 1)
	scope := &vmExecSessionStopScope{ctx: stopCtx, parent: ctx, cancel: cancel, done: done}
	go func() {
		waitErr := lease.waitStop(stopCtx)
		if stopCtx.Err() == nil {
			primary := vmExecSessionStopError(waitErr)
			cancel(primary)
			if context.Cause(stopCtx) == primary {
				scope.mu.Lock()
				scope.stopErr = primary
				scope.mu.Unlock()
			}
		}
		done <- struct{}{}
	}()
	return scope
}

func vmExecSessionStopError(waitErr error) *errs.DomainError {
	if waitErr == nil {
		return errs.New(
			errs.CodeProcessError,
			"retained VM-exec session relay deadline expired",
			errs.WithClass(errs.ClassInternal),
		)
	}
	if domainErr := errs.AsDomainError(waitErr); domainErr != nil {
		return domainErr
	}
	return errs.WrapMsg(
		errs.CodeProcessError,
		"retained VM-exec session stop failed",
		waitErr,
		errs.WithClass(errs.ClassInternal),
	)
}

func (scope *vmExecSessionStopScope) finish() {
	if scope == nil {
		return
	}
	scope.finishOnce.Do(func() {
		scope.cancel(nil)
		<-scope.done
	})
}

func (scope *vmExecSessionStopScope) finishIfInterrupted() error {
	if scope == nil || scope.ctx.Err() == nil {
		return nil
	}
	return scope.finishAndPrimary(nil)
}

func (scope *vmExecSessionStopScope) finishAndPrimary(operationErr error) error {
	scope.finish()
	scope.mu.Lock()
	stopErr := scope.stopErr
	scope.mu.Unlock()
	if stopErr != nil {
		return withVMExecSessionResponseEvidence(stopErr, operationErr)
	}
	if cause := context.Cause(scope.parent); cause != nil {
		if domainErr := errs.AsDomainError(cause); domainErr != nil {
			return withVMExecSessionResponseEvidence(domainErr, operationErr)
		}
		interruptionErr := errs.WrapMsg(
			errs.CodeProcessError,
			"retained VM-exec session interrupted",
			cause,
			errs.WithClass(errs.ClassInternal),
		)
		return withVMExecSessionResponseEvidence(interruptionErr, operationErr)
	}
	return operationErr
}

func withVMExecSessionResponseEvidence(primary *errs.DomainError, operationErr error) *errs.DomainError {
	operationDomain := errs.AsDomainError(operationErr)
	if primary == nil || operationDomain == nil {
		return primary
	}
	responseStarted, ok := operationDomain.Details["response_started"].(bool)
	if !ok {
		return primary
	}
	details := make(map[string]any, len(primary.Details)+1)
	for key, value := range primary.Details {
		details[key] = value
	}
	details["response_started"] = responseStarted
	return &errs.DomainError{
		Code:    primary.Code,
		Class:   primary.Class,
		Message: primary.Message,
		Err:     errors.Join(primary, operationErr),
		Op:      primary.Op,
		Entity:  primary.Entity,
		Details: details,
	}
}

func validateVMExecSessionHandlerInputs(
	ctx context.Context,
	endpoint controlEndpoint,
	deps vmExecSessionHandlerDeps,
) error {
	if ctx == nil {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec session context is required")
	}
	if endpoint == nil {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec session control endpoint is required")
	}
	if deps.control.now == nil || deps.control.shutdownWrite == nil {
		return errs.New(errs.CodeInternal, "retained VM-exec session control dependencies are not active")
	}
	reader := deps.connectionReader
	if reader.recvmsg == nil || reader.poll == nil || reader.fstat == nil || reader.descriptorFlags == nil ||
		reader.socketDomain == nil || reader.socketType == nil || reader.accepting == nil || reader.socketError == nil ||
		reader.socketName == nil || reader.peerName == nil || reader.closeFD == nil || reader.shutdownFD == nil {
		return errs.New(errs.CodeInternal, "retained VM-exec session connection dependencies are not active")
	}
	return nil
}

func (resources *vmExecSessionResources) cleanup(ctx context.Context) error {
	if resources == nil {
		return nil
	}
	resources.mu.Lock()
	defer resources.mu.Unlock()
	if resources.cleaned {
		return resources.cleanupErr
	}
	resources.cleaned = true
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	if resources.destination != nil {
		resources.cleanupErr = errors.Join(
			resources.cleanupErr,
			resources.destination.shutdownAndClose(cleanupCtx),
		)
	}
	if resources.source != nil {
		resources.cleanupErr = errors.Join(
			resources.cleanupErr,
			resources.source.shutdownAndClose(cleanupCtx),
		)
	}
	if resources.lease != nil {
		resources.cleanupErr = errors.Join(resources.cleanupErr, resources.lease.release(cleanupCtx))
	}
	return resources.cleanupErr
}

func failVMExecSession(
	ctx context.Context,
	endpoint controlEndpoint,
	resources *vmExecSessionResources,
	primary error,
	deps controlDeps,
) error {
	cleanupErr := resources.cleanup(ctx)
	operationErr := withVMExecSessionCleanupEvidence(primary, cleanupErr)
	return deliverVMExecFailure(ctx, endpoint, operationErr, deps)
}

func withVMExecSessionCleanupEvidence(primary error, cleanupErr error) error {
	if cleanupErr == nil {
		return primary
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		domainErr = errs.New(
			errs.CodeProcessError,
			"retained VM-exec session cleanup failed",
			errs.WithClass(errs.ClassInternal),
		)
	}
	// DomainError.Details is the repository's intentional closed sum type. The sidecar wire codec independently
	// admits only bounded booleans and integers from this map before anything crosses the control boundary.
	details := make(map[string]any, len(domainErr.Details)+1)
	for key, value := range domainErr.Details {
		details[key] = value
	}
	details["cleanup_failed"] = true
	return &errs.DomainError{
		Code:    domainErr.Code,
		Class:   domainErr.Class,
		Message: domainErr.Message,
		Err:     errors.Join(primary, cleanupErr),
		Op:      domainErr.Op,
		Entity:  domainErr.Entity,
		Details: details,
	}
}

func deliverVMExecSourceRetained(
	ctx context.Context,
	endpoint controlEndpoint,
	deps controlDeps,
) error {
	deadline := boundedResponseDeadline(ctx, deps.now())
	responseCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := endpoint.SetWriteDeadline(deadline); err != nil {
		return vmExecSessionResponseTransportError(err, false)
	}
	writer := &responseStartWriter{writer: endpoint}
	if err := vmexecsessionwire.EncodeSourceRetained(responseCtx, writer); err != nil {
		return vmExecSessionResponseTransportError(err, writer.started)
	}
	return nil
}

func deliverVMExecReady(ctx context.Context, endpoint controlEndpoint, deps controlDeps) error {
	deadline := boundedResponseDeadline(ctx, deps.now())
	responseCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := endpoint.SetWriteDeadline(deadline); err != nil {
		return vmExecSessionResponseTransportError(err, false)
	}
	writer := &responseStartWriter{writer: endpoint}
	if err := vmexecsessionwire.EncodeReady(responseCtx, writer); err != nil {
		return vmExecSessionResponseTransportError(err, writer.started)
	}
	return nil
}

func deliverVMExecReleased(ctx context.Context, endpoint controlEndpoint, deps controlDeps) error {
	deadline := boundedResponseDeadline(ctx, deps.now())
	responseCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	if err := endpoint.SetWriteDeadline(deadline); err != nil {
		return vmExecSessionResponseTransportError(err, false)
	}
	writer := &responseStartWriter{writer: endpoint}
	if err := vmexecsessionwire.EncodeReleased(responseCtx, writer); err != nil {
		return vmExecSessionResponseTransportError(err, writer.started)
	}
	if err := deps.shutdownWrite(responseCtx, int(endpoint.Fd())); err != nil {
		return vmExecSessionResponseTransportError(err, true)
	}
	return nil
}

func deliverVMExecFailure(
	ctx context.Context,
	endpoint controlEndpoint,
	operationErr error,
	deps controlDeps,
) error {
	deadline := boundedResponseDeadline(ctx, deps.now())
	responseCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	if err := endpoint.SetWriteDeadline(deadline); err != nil {
		return vmExecSessionResponseTransportError(errors.Join(operationErr, err), false)
	}
	writer := &responseStartWriter{writer: endpoint}
	if err := vmexecsessionwire.EncodeFailure(responseCtx, writer, operationErr); err != nil {
		return vmExecSessionResponseTransportError(errors.Join(operationErr, err), writer.started)
	}
	if err := deps.shutdownWrite(responseCtx, int(endpoint.Fd())); err != nil {
		return vmExecSessionResponseTransportError(errors.Join(operationErr, err), true)
	}
	return nil
}

func requireVMExecSessionReleaseEOF(ctx context.Context, reader io.Reader) error {
	var trailing [1]byte
	count, err := reader.Read(trailing[:])
	if contextErr := ctx.Err(); contextErr != nil {
		return errs.WrapMsg(errs.CodeProcessError, "read trailing retained VM-exec release input", contextErr)
	}
	if count > 0 || err == nil {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec release contains trailing input")
	}
	if err != io.EOF {
		return errs.WrapMsg(errs.CodeValidationFailed, "read trailing retained VM-exec release input", err)
	}
	return nil
}

func vmExecSessionResponseTransportError(cause error, responseStarted bool) error {
	return errs.WrapMsg(
		errs.CodeProcessError,
		"deliver retained VM-exec session response",
		cause,
		errs.WithClass(errs.ClassInternal),
		// DomainError.Details is the only repository error-evidence sum type; the value is a bounded boolean.
		errs.WithDetails(map[string]any{"response_started": responseStarted}),
	)
}
