package vsock

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

// DestinationSession owns one already selected destination transport and one host-built request.
// THREAD-SAFE: Read rejects concurrent readers while Cancel and Close may proceed concurrently with a read.
type DestinationSession struct {
	// mu guards requestID, response, openClaimed, opened, readInFlight, cancelClaimed, complete, closed, and deadline.
	mu sync.Mutex

	conn          net.Conn
	requestID     string
	response      *vmexecwire.ResponseReader
	openClaimed   bool
	opened        bool
	readInFlight  bool
	cancelClaimed bool
	complete      bool
	closed        bool
	deadline      time.Time
	done          chan struct{}
	closeOnce     sync.Once
	closeErr      error
	// The clock and timer seams remain immutable after Open starts so lifetime expiry stays race-free.
	now              func() time.Time
	newLifetimeTimer func(time.Time) (<-chan time.Time, func())
}

// NewDestinationSession creates a single-use session over an already selected destination connection.
func NewDestinationSession(conn net.Conn) (*DestinationSession, error) {
	if conn == nil {
		return nil, errs.New(errs.CodeValidationFailed, "destination session requires a selected connection")
	}
	return &DestinationSession{
		conn:             conn,
		done:             make(chan struct{}),
		now:              time.Now,
		newLifetimeTimer: destinationLifetimeTimer,
	}, nil
}

// Open sends the session's only validated host-built destination request.
func (session *DestinationSession) Open(ctx context.Context, request vmexecwire.DestinationRequest) error {
	session.mu.Lock()
	if session.openClaimed {
		session.mu.Unlock()
		return errs.New(errs.CodeValidationFailed, "destination session permits a single open attempt")
	}
	if session.closed {
		session.mu.Unlock()
		return errs.New(errs.CodeValidationFailed, "destination session is closed")
	}
	session.openClaimed = true
	deadline := effectiveDestinationDeadline(ctx, session.now())
	session.deadline = deadline
	session.mu.Unlock()

	if err := request.Validate(); err != nil {
		return session.fail(err, "open destination session")
	}
	if err := session.setWriteOperationDeadline(ctx); err != nil {
		return session.fail(err, "open destination session")
	}
	session.bindLifetime(ctx, deadline)
	if err := vmexecwire.EncodeDestinationRequest(ctx, session.conn, request); err != nil {
		return session.failContext(ctx, err, "open destination session")
	}
	response, err := vmexecwire.NewResponseReader(session.conn, request.RequestID)
	if err != nil {
		return session.fail(err, "open destination session")
	}

	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return session.fail(
			errs.New(errs.CodeProcessError, "destination session closed while opening"),
			"open destination session",
		)
	}
	session.requestID = request.RequestID
	session.response = response
	session.opened = true
	session.mu.Unlock()
	return nil
}

// Read returns the next output frame and withholds a terminal frame until physical EOF is verified.
func (session *DestinationSession) Read(ctx context.Context) (vmexecwire.ResponseFrame, error) {
	session.mu.Lock()
	if session.complete {
		session.mu.Unlock()
		return nil, io.EOF
	}
	if session.closed {
		session.mu.Unlock()
		return nil, errs.New(errs.CodeValidationFailed, "destination session is closed")
	}
	if !session.opened {
		session.mu.Unlock()
		return nil, errs.New(errs.CodeValidationFailed, "destination session is not open")
	}
	if session.readInFlight {
		session.mu.Unlock()
		return nil, errs.New(errs.CodeValidationFailed, "destination session permits a single reader")
	}
	session.readInFlight = true
	response := session.response
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.readInFlight = false
		session.mu.Unlock()
	}()

	if err := session.setReadOperationDeadline(ctx); err != nil {
		return nil, session.fail(err, "read destination session response")
	}
	stopContextWatch := session.watchOperationContext(ctx)
	defer stopContextWatch()

	frame, err := response.Read(ctx)
	if err != nil {
		return nil, session.failContext(ctx, err, "read destination session response")
	}
	if !destinationTerminalFrame(frame) {
		return frame, nil
	}
	if _, err := response.Read(ctx); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errs.New(errs.CodeValidationFailed, "destination response did not end after terminal frame")
		}
		return nil, session.failContext(ctx, err, "verify destination session response EOF")
	}

	session.mu.Lock()
	session.complete = true
	session.mu.Unlock()
	return frame, nil
}

// Cancel writes the session's only control frame with the active request identity.
func (session *DestinationSession) Cancel(ctx context.Context) error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return errs.New(errs.CodeValidationFailed, "destination session is closed")
	}
	if !session.opened {
		session.mu.Unlock()
		return errs.New(errs.CodeValidationFailed, "destination session is not open")
	}
	if session.cancelClaimed {
		session.mu.Unlock()
		return errs.New(errs.CodeValidationFailed, "destination session permits a single cancellation")
	}
	session.cancelClaimed = true
	requestID := session.requestID
	session.mu.Unlock()

	if err := session.setWriteOperationDeadline(ctx); err != nil {
		return session.fail(err, "cancel destination session")
	}
	stopContextWatch := session.watchOperationContext(ctx)
	defer stopContextWatch()
	if err := vmexecwire.WriteCancel(ctx, session.conn, requestID); err != nil {
		return session.failContext(ctx, err, "cancel destination session")
	}
	return nil
}

// Close releases the dedicated destination transport exactly once.
func (session *DestinationSession) Close() error {
	session.closeOnce.Do(func() {
		session.mu.Lock()
		session.closed = true
		session.mu.Unlock()
		if err := session.conn.Close(); err != nil {
			session.closeErr = errs.WrapMsg(
				errs.CodeProcessError,
				"close destination session",
				err,
				errs.WithClass(errs.ClassInternal),
			)
		}
		close(session.done)
	})
	return session.closeErr
}

func destinationTerminalFrame(frame vmexecwire.ResponseFrame) bool {
	switch frame.(type) {
	case vmexecwire.ResultFrame, vmexecwire.ErrorFrame:
		return true
	default:
		return false
	}
}

func effectiveDestinationDeadline(ctx context.Context, now time.Time) time.Time {
	deadline := now.Add(vmexecwire.MaxExecutionDuration)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func (session *DestinationSession) operationDeadline(ctx context.Context) (time.Time, error) {
	session.mu.Lock()
	deadline := session.deadline
	closed := session.closed
	session.mu.Unlock()
	if closed {
		return time.Time{}, errs.New(
			errs.CodeProcessError,
			"destination session is closed",
			errs.WithClass(errs.ClassInternal),
		)
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return deadline, nil
}

func (session *DestinationSession) setReadOperationDeadline(ctx context.Context) error {
	deadline, err := session.operationDeadline(ctx)
	if err != nil {
		return err
	}
	if err := session.conn.SetReadDeadline(deadline); err != nil {
		return errs.WrapMsg(
			errs.CodeProcessError,
			"set destination session read deadline",
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	return nil
}

func (session *DestinationSession) setWriteOperationDeadline(ctx context.Context) error {
	deadline, err := session.operationDeadline(ctx)
	if err != nil {
		return err
	}
	if err := session.conn.SetWriteDeadline(deadline); err != nil {
		return errs.WrapMsg(
			errs.CodeProcessError,
			"set destination session write deadline",
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	return nil
}

func destinationLifetimeTimer(deadline time.Time) (<-chan time.Time, func()) {
	timer := time.NewTimer(time.Until(deadline))
	return timer.C, func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func (session *DestinationSession) bindLifetime(ctx context.Context, deadline time.Time) {
	expired, stopTimer := session.newLifetimeTimer(deadline)
	go func() {
		defer stopTimer()
		select {
		case <-ctx.Done():
			// The opening context owns the destination session lifetime.
			_ = session.Close()
		case <-expired:
			// The fixed deadline releases idle sessions even when the opening context remains live.
			_ = session.Close()
		case <-session.done:
		}
	}()
}

func (session *DestinationSession) watchOperationContext(ctx context.Context) func() {
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Closing is the only portable way to interrupt a partial framed read or write.
			_ = session.Close()
		case <-finished:
		case <-session.done:
		}
	}()
	return func() { close(finished) }
}

func (session *DestinationSession) failContext(ctx context.Context, err error, message string) error {
	if ctx.Err() != nil {
		err = errs.WrapMsg(
			errs.CodeProcessError,
			message,
			ctx.Err(),
			errs.WithClass(errs.ClassInternal),
		)
	}
	return session.fail(err, message)
}

func (session *DestinationSession) fail(err error, message string) error {
	err = normalizeDestinationSessionError(err, message)
	closeErr := session.Close()
	if closeErr == nil {
		return err
	}
	return errs.WrapMsg(
		errs.CodeProcessError,
		message,
		errors.Join(err, closeErr),
		errs.WithClass(errs.ClassInternal),
	)
}

func normalizeDestinationSessionError(err error, message string) error {
	var domainErr *errs.DomainError
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case errs.CodeValidationFailed:
			if domainErr.Class == errs.ClassValidation {
				return err
			}
			return errs.WrapMsg(
				errs.CodeValidationFailed,
				domainErr.Message,
				err,
				errs.WithClass(errs.ClassValidation),
			)
		case errs.CodeProcessError:
			if domainErr.Class == errs.ClassInternal {
				return err
			}
		}
	}
	return errs.WrapMsg(
		errs.CodeProcessError,
		message,
		err,
		errs.WithClass(errs.ClassInternal),
	)
}
