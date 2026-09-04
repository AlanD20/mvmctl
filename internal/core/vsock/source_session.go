package vsock

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

// SourceSession owns one already authority-admitted source transport and one VM-exec source request.
type SourceSession struct {
	// mu guards sessionID, requestID, response, opened, read, cancelClaimed, closed, and deadline.
	mu sync.Mutex

	conn          net.Conn
	token         string
	sessionID     string
	requestID     string
	response      *vmexecwire.ResponseWriter
	opened        bool
	read          bool
	cancelClaimed bool
	closed        bool
	deadline      time.Time
	done          chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

// NewSourceSession creates a single-use session over an already authority-admitted connection.
func NewSourceSession(conn net.Conn, agentToken string) (*SourceSession, error) {
	if conn == nil {
		return nil, errs.New(errs.CodeValidationFailed, "source session requires an admitted connection")
	}
	probe := vmexecwire.SourceBootstrap{
		AgentToken:      agentToken,
		SourceSessionID: "00112233445566778899aabbccddeeff",
	}
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	return &SourceSession{conn: conn, token: agentToken, done: make(chan struct{})}, nil
}

// Open generates an identity, authenticates the source transport, and waits for its exact ready response.
func (session *SourceSession) Open(ctx context.Context) (string, error) {
	session.mu.Lock()
	if session.opened {
		session.mu.Unlock()
		return "", errs.New(errs.CodeValidationFailed, "source session is already opened")
	}
	if session.closed {
		session.mu.Unlock()
		return "", errs.New(errs.CodeValidationFailed, "source session is closed")
	}
	session.opened = true
	session.deadline = effectiveSourceDeadline(ctx)
	session.mu.Unlock()

	if err := session.conn.SetDeadline(session.deadline); err != nil {
		return "", errors.Join(
			errs.WrapMsg(errs.CodeProcessError, "set source session deadline", err),
			session.Close(),
		)
	}
	session.bindLifetime(ctx)

	sessionID, err := vmexecwire.NewSourceSessionID()
	if err != nil {
		return "", errors.Join(err, session.Close())
	}
	if err := vmexecwire.EncodeSourceBootstrap(ctx, session.conn, vmexecwire.SourceBootstrap{
		AgentToken:      session.token,
		SourceSessionID: sessionID,
	}); err != nil {
		return "", errors.Join(err, session.Close())
	}
	if _, err := vmexecwire.DecodeSourceReady(ctx, session.conn, sessionID); err != nil {
		if ctx.Err() != nil {
			err = errs.WrapMsg(errs.CodeProcessError, "open source session", ctx.Err())
		}
		return "", errors.Join(err, session.Close())
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return "", errors.Join(
			errs.New(errs.CodeProcessError, "source session closed during bootstrap"),
			session.Close(),
		)
	}
	session.sessionID = sessionID
	session.mu.Unlock()
	return sessionID, nil
}

// ReadRequest reads and binds the session's only guest-source request.
func (session *SourceSession) ReadRequest(ctx context.Context) (vmexecwire.SourceRequest, error) {
	session.mu.Lock()
	if !session.opened || session.sessionID == "" {
		session.mu.Unlock()
		return vmexecwire.SourceRequest{}, errs.New(errs.CodeValidationFailed, "source session is not open")
	}
	if session.read {
		session.mu.Unlock()
		return vmexecwire.SourceRequest{}, errs.New(
			errs.CodeValidationFailed,
			"source session permits a single request",
		)
	}
	if session.closed {
		session.mu.Unlock()
		return vmexecwire.SourceRequest{}, errs.New(errs.CodeValidationFailed, "source session is closed")
	}
	session.read = true
	session.mu.Unlock()
	if err := session.setReadOperationDeadline(ctx); err != nil {
		return vmexecwire.SourceRequest{}, errors.Join(err, session.Close())
	}
	stopContextWatch := session.watchOperationContext(ctx)
	defer stopContextWatch()

	request, err := vmexecwire.DecodeSourceRequest(ctx, session.conn)
	if err != nil {
		if ctx.Err() != nil {
			err = errs.WrapMsg(errs.CodeProcessError, "read source session request", ctx.Err())
		}
		return vmexecwire.SourceRequest{}, errors.Join(err, session.Close())
	}
	response, err := vmexecwire.NewResponseWriter(session.conn, request.RequestID)
	if err != nil {
		return vmexecwire.SourceRequest{}, errors.Join(err, session.Close())
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return vmexecwire.SourceRequest{}, errors.Join(
			errs.New(errs.CodeProcessError, "source session closed while reading request"),
			session.Close(),
		)
	}
	session.requestID = request.RequestID
	session.response = response
	session.mu.Unlock()
	return request, nil
}

// WaitCancel reads the session's only cancellation control frame for its active request.
func (session *SourceSession) WaitCancel(ctx context.Context) error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return errs.New(errs.CodeValidationFailed, "source session is closed")
	}
	if session.requestID == "" {
		session.mu.Unlock()
		return errs.New(errs.CodeValidationFailed, "source session request has not been read")
	}
	if session.cancelClaimed {
		session.mu.Unlock()
		return errs.New(errs.CodeValidationFailed, "source session permits a single cancellation waiter")
	}
	session.cancelClaimed = true
	requestID := session.requestID
	session.mu.Unlock()

	if err := session.setReadOperationDeadline(ctx); err != nil {
		return errors.Join(err, session.Close())
	}
	stopContextWatch := session.watchOperationContext(ctx)
	defer stopContextWatch()

	if err := vmexecwire.DecodeCancel(ctx, session.conn, requestID); err != nil {
		if ctx.Err() != nil {
			err = errs.WrapMsg(errs.CodeProcessError, "wait for source session cancellation", ctx.Err())
		}
		return errors.Join(err, session.Close())
	}
	return nil
}

// WriteStdout writes one bounded stdout response frame.
func (session *SourceSession) WriteStdout(ctx context.Context, data []byte) error {
	response, err := session.responseWriter()
	if err != nil {
		return err
	}
	if err := session.setWriteOperationDeadline(ctx); err != nil {
		return err
	}
	stopContextWatch := session.watchOperationContext(ctx)
	defer stopContextWatch()
	return response.WriteStdout(ctx, data)
}

// WriteStderr writes one bounded stderr response frame.
func (session *SourceSession) WriteStderr(ctx context.Context, data []byte) error {
	response, err := session.responseWriter()
	if err != nil {
		return err
	}
	if err := session.setWriteOperationDeadline(ctx); err != nil {
		return err
	}
	stopContextWatch := session.watchOperationContext(ctx)
	defer stopContextWatch()
	return response.WriteStderr(ctx, data)
}

// WriteResult writes the terminal result and closes the physical transport so the peer observes EOF.
func (session *SourceSession) WriteResult(ctx context.Context, exitCode int, duration time.Duration) error {
	response, err := session.responseWriter()
	if err != nil {
		return err
	}
	if err := session.setWriteOperationDeadline(ctx); err != nil {
		return errors.Join(err, session.Close())
	}
	stopContextWatch := session.watchOperationContext(ctx)
	writeErr := response.WriteResult(ctx, exitCode, duration)
	stopContextWatch()
	return errors.Join(writeErr, session.Close())
}

// WriteError writes the terminal error and closes the physical transport so the peer observes EOF.
func (session *SourceSession) WriteError(ctx context.Context, message string) error {
	response, err := session.responseWriter()
	if err != nil {
		return err
	}
	if err := session.setWriteOperationDeadline(ctx); err != nil {
		return errors.Join(err, session.Close())
	}
	stopContextWatch := session.watchOperationContext(ctx)
	writeErr := response.WriteError(ctx, message)
	stopContextWatch()
	return errors.Join(writeErr, session.Close())
}

func (session *SourceSession) responseWriter() (*vmexecwire.ResponseWriter, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, errs.New(errs.CodeValidationFailed, "source session is closed")
	}
	if session.response == nil {
		return nil, errs.New(errs.CodeValidationFailed, "source session request has not been read")
	}
	return session.response, nil
}

// Close releases the dedicated source transport exactly once.
func (session *SourceSession) Close() error {
	session.closeOnce.Do(func() {
		session.mu.Lock()
		session.closed = true
		session.mu.Unlock()
		session.closeErr = session.conn.Close()
		close(session.done)
	})
	return session.closeErr
}

func effectiveSourceDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(vmexecwire.MaxExecutionDuration)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func (session *SourceSession) operationDeadline(ctx context.Context) (time.Time, error) {
	session.mu.Lock()
	deadline := session.deadline
	closed := session.closed
	session.mu.Unlock()
	if closed {
		return time.Time{}, errs.New(errs.CodeProcessError, "source session is closed")
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return deadline, nil
}

func (session *SourceSession) setReadOperationDeadline(ctx context.Context) error {
	deadline, err := session.operationDeadline(ctx)
	if err != nil {
		return err
	}
	if err := session.conn.SetReadDeadline(deadline); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "set source session read deadline", err)
	}
	return nil
}

func (session *SourceSession) setWriteOperationDeadline(ctx context.Context) error {
	deadline, err := session.operationDeadline(ctx)
	if err != nil {
		return err
	}
	if err := session.conn.SetWriteDeadline(deadline); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "set source session write deadline", err)
	}
	return nil
}

func (session *SourceSession) bindLifetime(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			// The opening context owns the source session lifetime.
			_ = session.Close()
		case <-session.done:
		}
	}()
}

func (session *SourceSession) watchOperationContext(ctx context.Context) func() {
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Closing is the only portable way to interrupt a partial framed read.
			_ = session.Close()
		case <-finished:
		case <-session.done:
		}
	}()
	return func() { close(finished) }
}
