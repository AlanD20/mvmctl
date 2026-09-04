package vsock

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

const sourceSessionTestToken = "123e4567-e89b-42d3-a456-426614174000"

func TestSourceSessionUsesAnAdmittedConnectionForOneRequest(t *testing.T) {
	hostConn, agentConn := net.Pipe()
	t.Cleanup(func() { _ = agentConn.Close() })
	session, err := NewSourceSession(hostConn, sourceSessionTestToken)
	require.NoError(t, err)

	requestID := "00112233445566778899aabbccddeeff"
	agentDone := make(chan error, 1)
	go func() {
		bootstrap, decodeErr := vmexecwire.DecodeSourceBootstrap(context.Background(), agentConn)
		if decodeErr != nil {
			agentDone <- decodeErr
			return
		}
		if encodeErr := vmexecwire.EncodeSourceReady(context.Background(), agentConn, vmexecwire.SourceReady{
			SourceSessionID: bootstrap.SourceSessionID,
		}); encodeErr != nil {
			agentDone <- encodeErr
			return
		}
		if encodeErr := vmexecwire.EncodeSourceRequest(context.Background(), agentConn, vmexecwire.SourceRequest{
			RequestID: requestID, Destination: "target", TargetUser: "alice", Command: "printf hi",
		}); encodeErr != nil {
			agentDone <- encodeErr
			return
		}
		reader, readerErr := vmexecwire.NewResponseReader(agentConn, requestID)
		if readerErr != nil {
			agentDone <- readerErr
			return
		}
		frame, readErr := reader.Read(context.Background())
		if readErr != nil {
			agentDone <- readErr
			return
		}
		stdout, ok := frame.(vmexecwire.StdoutFrame)
		if !ok || string(stdout.Data) != "hi" {
			agentDone <- errors.New("unexpected stdout frame")
			return
		}
		frame, readErr = reader.Read(context.Background())
		if readErr != nil {
			agentDone <- readErr
			return
		}
		result, ok := frame.(vmexecwire.ResultFrame)
		if !ok || result.ExitCode != 0 {
			agentDone <- errors.New("unexpected result frame")
			return
		}
		_, readErr = reader.Read(context.Background())
		if !errors.Is(readErr, io.EOF) {
			agentDone <- errors.New("response did not end in physical EOF")
			return
		}
		agentDone <- nil
	}()

	sessionID, err := session.Open(t.Context())
	require.NoError(t, err)
	assert.Len(t, sessionID, 32)
	request, err := session.ReadRequest(t.Context())
	require.NoError(t, err)
	assert.Equal(t, requestID, request.RequestID)
	require.NoError(t, session.WriteStdout(t.Context(), []byte("hi")))
	require.NoError(t, session.WriteResult(t.Context(), 0, time.Millisecond))
	assert.NoError(t, <-agentDone)

	_, err = session.ReadRequest(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single request")
	_, err = session.Open(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already opened")
}

func TestSourceSessionWaitCancelAcceptsMatchingRequest(t *testing.T) {
	hostConn, agentConn := net.Pipe()
	requestID := "00112233445566778899aabbccddeeff"
	session := openSourceSessionWithRequest(t, hostConn, agentConn, requestID)
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- vmexecwire.WriteCancel(context.Background(), agentConn, requestID)
	}()

	assert.NoError(t, session.WaitCancel(t.Context()))
	assert.NoError(t, <-agentDone)
}

func TestSourceSessionWaitCancelRejectsInvalidFrames(t *testing.T) {
	requestID := "00112233445566778899aabbccddeeff"
	wrongRequestID := "ffeeddccbbaa99887766554433221100"
	malformed := sourceSessionControlFrameForTest(
		`{"schema_version":1,"request_id":"` + requestID + `","type":"cancel"}`,
	)
	malformed[0] = 'X'
	tests := map[string][]byte{
		"wrong request ID": sourceSessionControlFrameForTest(
			`{"schema_version":1,"request_id":"` + wrongRequestID + `","type":"cancel"}`,
		),
		"malformed frame": malformed,
		"non-cancel frame": sourceSessionControlFrameForTest(
			`{"schema_version":1,"request_id":"` + requestID + `","type":"result"}`,
		),
		"trailing JSON": sourceSessionControlFrameForTest(
			`{"schema_version":1,"request_id":"` + requestID + `","type":"cancel"}{}`,
		),
	}

	for name, frame := range tests {
		t.Run(name, func(t *testing.T) {
			hostConn, agentConn := net.Pipe()
			session := openSourceSessionWithRequest(t, hostConn, agentConn, requestID)
			writeDone := make(chan error, 1)
			go func() {
				_, writeErr := agentConn.Write(frame)
				writeDone <- writeErr
			}()

			err := session.WaitCancel(t.Context())
			require.Error(t, err)
			assertSourceSessionErrorCode(t, err, errs.CodeValidationFailed)
			// Invalid prefixes may close the transport before the peer finishes the rejected frame.
			<-writeDone
		})
	}
}

func TestSourceSessionWaitCancelRejectsInvalidLifecycle(t *testing.T) {
	t.Run("before request", func(t *testing.T) {
		hostConn, agentConn := net.Pipe()
		t.Cleanup(func() { _ = agentConn.Close() })
		session, err := NewSourceSession(hostConn, sourceSessionTestToken)
		require.NoError(t, err)
		t.Cleanup(func() { _ = session.Close() })

		err = session.WaitCancel(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request has not been read")
		assertSourceSessionErrorCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("after close", func(t *testing.T) {
		hostConn, agentConn := net.Pipe()
		session := openSourceSessionWithRequest(
			t,
			hostConn,
			agentConn,
			"00112233445566778899aabbccddeeff",
		)
		require.NoError(t, session.Close())

		err := session.WaitCancel(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source session is closed")
		assertSourceSessionErrorCode(t, err, errs.CodeValidationFailed)
	})
}

func TestSourceSessionWaitCancelPermitsOneWaiter(t *testing.T) {
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledSourceConn{Conn: hostPipe, readStarted: make(chan struct{})}
	requestID := "00112233445566778899aabbccddeeff"
	session := openSourceSessionWithRequest(t, hostConn, agentConn, requestID)
	hostConn.observeReads.Store(true)
	firstResult := make(chan error, 1)
	go func() { firstResult <- session.WaitCancel(t.Context()) }()
	select {
	case <-hostConn.readStarted:
	case <-time.After(time.Second):
		t.Fatal("first cancellation waiter did not reach the transport")
	}

	err := session.WaitCancel(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single cancellation waiter")
	assertSourceSessionErrorCode(t, err, errs.CodeValidationFailed)

	writeDone := make(chan error, 1)
	go func() { writeDone <- vmexecwire.WriteCancel(context.Background(), agentConn, requestID) }()
	select {
	case err = <-firstResult:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("first cancellation waiter did not consume the matching frame")
	}
	assert.NoError(t, <-writeDone)

	err = session.WaitCancel(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single cancellation waiter")
	assertSourceSessionErrorCode(t, err, errs.CodeValidationFailed)
}

func TestSourceSessionCanceledPartialCancelClosesTransport(t *testing.T) {
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledSourceConn{Conn: hostPipe, readStarted: make(chan struct{})}
	session := openSourceSessionWithRequest(
		t,
		hostConn,
		agentConn,
		"00112233445566778899aabbccddeeff",
	)
	hostConn.observeReads.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- session.WaitCancel(ctx) }()
	select {
	case <-hostConn.readStarted:
	case <-time.After(time.Second):
		t.Fatal("cancellation waiter did not reach the transport")
	}
	_, err := agentConn.Write([]byte("MVM"))
	require.NoError(t, err)
	cancel()
	select {
	case waitErr := <-result:
		require.Error(t, waitErr)
		assert.ErrorIs(t, waitErr, context.Canceled)
		assertSourceSessionErrorCode(t, waitErr, errs.CodeProcessError)
	case <-time.After(time.Second):
		t.Fatal("partial cancellation frame outlived context cancellation")
	}
	_, err = agentConn.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

func TestSourceSessionCloseUnblocksPartialCancel(t *testing.T) {
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledSourceConn{Conn: hostPipe, readStarted: make(chan struct{})}
	session := openSourceSessionWithRequest(
		t,
		hostConn,
		agentConn,
		"00112233445566778899aabbccddeeff",
	)
	hostConn.observeReads.Store(true)
	result := make(chan error, 1)
	go func() { result <- session.WaitCancel(context.Background()) }()
	select {
	case <-hostConn.readStarted:
	case <-time.After(time.Second):
		t.Fatal("cancellation waiter did not reach the transport")
	}
	_, err := agentConn.Write([]byte("MVM"))
	require.NoError(t, err)
	require.NoError(t, session.Close())
	select {
	case waitErr := <-result:
		require.Error(t, waitErr)
		assertSourceSessionErrorCode(t, waitErr, errs.CodeValidationFailed)
	case <-time.After(time.Second):
		t.Fatal("partial cancellation frame outlived session close")
	}
	_, err = agentConn.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

func TestSourceSessionWaitCancelPreservesConcurrentResponseWrite(t *testing.T) {
	// Rationale: a control read and response write must not overwrite each other's directional deadlines.
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledSourceConn{Conn: hostPipe, readStarted: make(chan struct{})}
	requestID := "00112233445566778899aabbccddeeff"
	session := openSourceSessionWithRequest(t, hostConn, agentConn, requestID)
	hostConn.rejectCombinedDeadline.Store(true)
	hostConn.observeReads.Store(true)
	waitDone := make(chan error, 1)
	go func() { waitDone <- session.WaitCancel(t.Context()) }()
	select {
	case <-hostConn.readStarted:
	case <-time.After(time.Second):
		t.Fatal("cancellation waiter did not reach the transport")
	}

	cancelDone := make(chan error, 1)
	go func() { cancelDone <- vmexecwire.WriteCancel(context.Background(), agentConn, requestID) }()
	responseDone := make(chan error, 1)
	go func() {
		reader, readerErr := vmexecwire.NewResponseReader(agentConn, requestID)
		if readerErr != nil {
			responseDone <- readerErr
			return
		}
		frame, readerErr := reader.Read(context.Background())
		if readerErr != nil {
			responseDone <- readerErr
			return
		}
		stdout, ok := frame.(vmexecwire.StdoutFrame)
		if !ok || string(stdout.Data) != "still-running" {
			responseDone <- errors.New("unexpected concurrent stdout frame")
			return
		}
		responseDone <- nil
	}()

	assert.NoError(t, session.WriteStdout(t.Context(), []byte("still-running")))
	assert.NoError(t, <-cancelDone)
	assert.NoError(t, <-waitDone)
	assert.NoError(t, <-responseDone)
}

func TestConcurrentSourceSessionsRemainIsolated(t *testing.T) {
	type outcome struct {
		sessionID string
		requestID string
		err       error
	}
	outcomes := make(chan outcome, 2)
	for index, requestID := range []string{
		"00112233445566778899aabbccddeeff",
		"ffeeddccbbaa99887766554433221100",
	} {
		hostConn, agentConn := net.Pipe()
		session, err := NewSourceSession(hostConn, sourceSessionTestToken)
		require.NoError(t, err)
		go func(index int, requestID string) {
			defer agentConn.Close()
			bootstrap, decodeErr := vmexecwire.DecodeSourceBootstrap(context.Background(), agentConn)
			if decodeErr == nil {
				decodeErr = vmexecwire.EncodeSourceReady(context.Background(), agentConn, vmexecwire.SourceReady{
					SourceSessionID: bootstrap.SourceSessionID,
				})
			}
			if decodeErr == nil {
				decodeErr = vmexecwire.EncodeSourceRequest(context.Background(), agentConn, vmexecwire.SourceRequest{
					RequestID: requestID, Destination: "vm", TargetUser: "alice", Command: "true",
				})
			}
			if decodeErr != nil {
				outcomes <- outcome{err: decodeErr}
			}
		}(index, requestID)
		go func(requestID string) {
			sessionID, openErr := session.Open(context.Background())
			if openErr != nil {
				outcomes <- outcome{err: openErr}
				return
			}
			request, readErr := session.ReadRequest(context.Background())
			_ = session.Close()
			if readErr != nil {
				outcomes <- outcome{err: readErr}
				return
			}
			outcomes <- outcome{sessionID: sessionID, requestID: request.RequestID}
		}(requestID)
	}

	first := <-outcomes
	second := <-outcomes
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	assert.NotEqual(t, first.sessionID, second.sessionID)
	assert.ElementsMatch(t,
		[]string{"00112233445566778899aabbccddeeff", "ffeeddccbbaa99887766554433221100"},
		[]string{first.requestID, second.requestID},
	)
}

func TestSourceSessionRejectsInvalidConstruction(t *testing.T) {
	_, err := NewSourceSession(nil, sourceSessionTestToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admitted connection")

	hostConn, peerConn := net.Pipe()
	t.Cleanup(func() { _ = hostConn.Close() })
	t.Cleanup(func() { _ = peerConn.Close() })
	_, err = NewSourceSession(hostConn, "invalid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "canonical lowercase UUIDv4")
}

func TestSourceSessionCanceledPartialOpenClosesTransport(t *testing.T) {
	hostConn, agentConn := net.Pipe()
	session, err := NewSourceSession(hostConn, sourceSessionTestToken)
	require.NoError(t, err)
	bootstrapRead := make(chan struct{})
	go func() {
		_, _ = vmexecwire.DecodeSourceBootstrap(context.Background(), agentConn)
		close(bootstrapRead)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, openErr := session.Open(ctx)
		result <- openErr
	}()
	<-bootstrapRead
	cancel()
	select {
	case openErr := <-result:
		require.Error(t, openErr)
	case <-time.After(time.Second):
		t.Fatal("partial source bootstrap outlived cancellation")
	}
	_, err = agentConn.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

func TestSourceSessionCanceledPartialRequestClosesTransport(t *testing.T) {
	hostConn, agentConn := net.Pipe()
	session, err := NewSourceSession(hostConn, sourceSessionTestToken)
	require.NoError(t, err)
	lifetime, stopLifetime := context.WithCancel(context.Background())
	t.Cleanup(stopLifetime)
	ready := make(chan struct{})
	go func() {
		bootstrap, decodeErr := vmexecwire.DecodeSourceBootstrap(context.Background(), agentConn)
		if decodeErr == nil {
			decodeErr = vmexecwire.EncodeSourceReady(context.Background(), agentConn, vmexecwire.SourceReady{
				SourceSessionID: bootstrap.SourceSessionID,
			})
		}
		close(ready)
	}()
	_, err = session.Open(lifetime)
	require.NoError(t, err)
	<-ready

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, readErr := session.ReadRequest(requestCtx)
		result <- readErr
	}()
	_, err = agentConn.Write([]byte("MVM"))
	require.NoError(t, err)
	cancelRequest()
	select {
	case readErr := <-result:
		require.Error(t, readErr)
	case <-time.After(time.Second):
		t.Fatal("partial source request outlived cancellation")
	}
}

func TestSourceSessionCanceledBlockedWritesCloseTransport(t *testing.T) {
	tests := map[string]func(context.Context, *SourceSession) error{
		"stdout": func(ctx context.Context, session *SourceSession) error {
			return session.WriteStdout(ctx, []byte("blocked"))
		},
		"stderr": func(ctx context.Context, session *SourceSession) error {
			return session.WriteStderr(ctx, []byte("blocked"))
		},
		"result": func(ctx context.Context, session *SourceSession) error {
			return session.WriteResult(ctx, 0, 0)
		},
		"error": func(ctx context.Context, session *SourceSession) error {
			return session.WriteError(ctx, "blocked")
		},
	}

	for name, write := range tests {
		t.Run(name, func(t *testing.T) {
			hostPipe, agentConn := net.Pipe()
			hostConn := &controlledSourceConn{Conn: hostPipe, writeStarted: make(chan struct{})}
			session, err := NewSourceSession(hostConn, sourceSessionTestToken)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })
			t.Cleanup(func() { _ = agentConn.Close() })

			go func() {
				bootstrap, decodeErr := vmexecwire.DecodeSourceBootstrap(context.Background(), agentConn)
				if decodeErr == nil {
					decodeErr = vmexecwire.EncodeSourceReady(context.Background(), agentConn, vmexecwire.SourceReady{
						SourceSessionID: bootstrap.SourceSessionID,
					})
				}
				if decodeErr == nil {
					_ = vmexecwire.EncodeSourceRequest(context.Background(), agentConn, vmexecwire.SourceRequest{
						RequestID: "00112233445566778899aabbccddeeff", Destination: "target",
						TargetUser: "alice", Command: "true",
					})
				}
			}()
			_, err = session.Open(t.Context())
			require.NoError(t, err)
			_, err = session.ReadRequest(t.Context())
			require.NoError(t, err)

			hostConn.observeWrites.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- write(ctx, session) }()
			select {
			case <-hostConn.writeStarted:
			case <-time.After(time.Second):
				t.Fatal("source write did not reach the blocked transport")
			}
			select {
			case err := <-result:
				t.Fatalf("source write returned before cancellation: %v", err)
			default:
			}
			cancel()
			select {
			case writeErr := <-result:
				require.Error(t, writeErr)
			case <-time.After(time.Second):
				t.Fatal("blocked source write outlived cancellation")
			}
		})
	}
}

func TestSourceSessionTerminalWriteFailureClosesOnceAndRepeatsCloseError(t *testing.T) {
	hostPipe, agentConn := net.Pipe()
	closeErr := errors.New("close failed")
	hostConn := &controlledSourceConn{Conn: hostPipe, closeErr: closeErr}
	session, err := NewSourceSession(hostConn, sourceSessionTestToken)
	require.NoError(t, err)
	requestID := "00112233445566778899aabbccddeeff"
	go func() {
		bootstrap, decodeErr := vmexecwire.DecodeSourceBootstrap(context.Background(), agentConn)
		if decodeErr == nil {
			decodeErr = vmexecwire.EncodeSourceReady(context.Background(), agentConn, vmexecwire.SourceReady{
				SourceSessionID: bootstrap.SourceSessionID,
			})
		}
		if decodeErr == nil {
			_ = vmexecwire.EncodeSourceRequest(context.Background(), agentConn, vmexecwire.SourceRequest{
				RequestID: requestID, Destination: "target", TargetUser: "alice", Command: "true",
			})
		}
	}()
	_, err = session.Open(t.Context())
	require.NoError(t, err)
	_, err = session.ReadRequest(t.Context())
	require.NoError(t, err)
	hostConn.failWrites.Store(true)

	err = session.WriteResult(t.Context(), 0, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, errControlledWrite)
	assert.ErrorIs(t, err, closeErr)
	assert.ErrorIs(t, session.Close(), closeErr)
	assert.Equal(t, int32(1), hostConn.closeCalls.Load())
}

// openSourceSessionWithRequest completes the authenticated handshake and binds one source request.
func openSourceSessionWithRequest(
	t *testing.T,
	hostConn net.Conn,
	agentConn net.Conn,
	requestID string,
) *SourceSession {
	t.Helper()
	t.Cleanup(func() { _ = agentConn.Close() })
	session, err := NewSourceSession(hostConn, sourceSessionTestToken)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	agentDone := make(chan error, 1)
	go func() {
		bootstrap, agentErr := vmexecwire.DecodeSourceBootstrap(context.Background(), agentConn)
		if agentErr == nil {
			agentErr = vmexecwire.EncodeSourceReady(context.Background(), agentConn, vmexecwire.SourceReady{
				SourceSessionID: bootstrap.SourceSessionID,
			})
		}
		if agentErr == nil {
			agentErr = vmexecwire.EncodeSourceRequest(context.Background(), agentConn, vmexecwire.SourceRequest{
				RequestID: requestID, Destination: "target", TargetUser: "alice", Command: "true",
			})
		}
		agentDone <- agentErr
	}()

	_, err = session.Open(t.Context())
	require.NoError(t, err)
	request, err := session.ReadRequest(t.Context())
	require.NoError(t, err)
	assert.Equal(t, requestID, request.RequestID)
	require.NoError(t, <-agentDone)
	return session
}

// sourceSessionControlFrameForTest creates a raw frame so the session boundary receives invalid control input.
func sourceSessionControlFrameForTest(body string) []byte {
	prefix := make([]byte, 14)
	copy(prefix, vmexecwire.FrameMagic)
	binary.BigEndian.PutUint16(prefix[8:10], 1)
	binary.BigEndian.PutUint32(prefix[10:14], uint32(len(body)))
	return append(prefix, []byte(body)...)
}

func assertSourceSessionErrorCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var domainErr *errs.DomainError
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, code, domainErr.Code)
}

var errControlledWrite = errors.New("controlled write failure")
var errCombinedSourceDeadline = errors.New("combined source deadline would couple full-duplex operations")

type controlledSourceConn struct {
	net.Conn
	failWrites             atomic.Bool
	observeWrites          atomic.Bool
	observeReads           atomic.Bool
	rejectCombinedDeadline atomic.Bool
	writeOnce              sync.Once
	readOnce               sync.Once
	writeStarted           chan struct{}
	readStarted            chan struct{}
	closeCalls             atomic.Int32
	closeErr               error
}

func (conn *controlledSourceConn) Read(value []byte) (int, error) {
	if conn.observeReads.Load() {
		conn.readOnce.Do(func() { close(conn.readStarted) })
	}
	return conn.Conn.Read(value)
}

func (conn *controlledSourceConn) Write(value []byte) (int, error) {
	if conn.failWrites.Load() {
		return 0, errControlledWrite
	}
	if conn.observeWrites.Load() {
		conn.writeOnce.Do(func() { close(conn.writeStarted) })
	}
	return conn.Conn.Write(value)
}

func (conn *controlledSourceConn) SetDeadline(deadline time.Time) error {
	if conn.rejectCombinedDeadline.Load() {
		return errCombinedSourceDeadline
	}
	return conn.Conn.SetDeadline(deadline)
}

func (conn *controlledSourceConn) Close() error {
	conn.closeCalls.Add(1)
	_ = conn.Conn.Close()
	return conn.closeErr
}
