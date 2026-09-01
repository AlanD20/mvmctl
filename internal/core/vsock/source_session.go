package vsock

import (
	"context"
	"net"
	"sync"
	"time"

	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

// SourceSession owns one already authority-admitted source transport and one VM-exec source request.
type SourceSession struct {
	mu sync.Mutex

	conn      net.Conn
	token     string
	sessionID string
	response  *vmexecwire.ResponseWriter
	opened    bool
	read      bool
	closed    bool
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
	return &SourceSession{conn: conn, token: agentToken}, nil
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
	session.mu.Unlock()

	sessionID, err := vmexecwire.NewSourceSessionID()
	if err != nil {
		_ = session.Close()
		return "", err
	}
	if err := vmexecwire.EncodeSourceBootstrap(ctx, session.conn, vmexecwire.SourceBootstrap{
		AgentToken:      session.token,
		SourceSessionID: sessionID,
	}); err != nil {
		_ = session.Close()
		return "", err
	}
	if _, err := vmexecwire.DecodeSourceReady(ctx, session.conn, sessionID); err != nil {
		_ = session.Close()
		return "", err
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return "", errs.New(errs.CodeProcessError, "source session closed during bootstrap")
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

	request, err := vmexecwire.DecodeSourceRequest(ctx, session.conn)
	if err != nil {
		_ = session.Close()
		return vmexecwire.SourceRequest{}, err
	}
	response, err := vmexecwire.NewResponseWriter(session.conn, request.RequestID)
	if err != nil {
		_ = session.Close()
		return vmexecwire.SourceRequest{}, err
	}
	session.mu.Lock()
	session.response = response
	session.mu.Unlock()
	return request, nil
}

// WriteStdout writes one bounded stdout response frame.
func (session *SourceSession) WriteStdout(ctx context.Context, data []byte) error {
	response, err := session.responseWriter()
	if err != nil {
		return err
	}
	return response.WriteStdout(ctx, data)
}

// WriteStderr writes one bounded stderr response frame.
func (session *SourceSession) WriteStderr(ctx context.Context, data []byte) error {
	response, err := session.responseWriter()
	if err != nil {
		return err
	}
	return response.WriteStderr(ctx, data)
}

// WriteResult writes the terminal result and closes the physical transport so the peer observes EOF.
func (session *SourceSession) WriteResult(ctx context.Context, exitCode int, duration time.Duration) error {
	response, err := session.responseWriter()
	if err != nil {
		return err
	}
	if err := response.WriteResult(ctx, exitCode, duration); err != nil {
		return err
	}
	return session.Close()
}

// WriteError writes the terminal error and closes the physical transport so the peer observes EOF.
func (session *SourceSession) WriteError(ctx context.Context, message string) error {
	response, err := session.responseWriter()
	if err != nil {
		return err
	}
	if err := response.WriteError(ctx, message); err != nil {
		return err
	}
	return session.Close()
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
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.closed = true
	conn := session.conn
	session.mu.Unlock()
	return conn.Close()
}
