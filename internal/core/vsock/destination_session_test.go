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

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

const (
	destinationSessionTestRequestID = "00112233445566778899aabbccddeeff"
	destinationSessionTestToken     = "123e4567-e89b-42d3-a456-426614174000"
	destinationSessionTestTimeout   = time.Second
)

func TestDestinationSessionPreservesBinaryOutputAndWithholdsTerminalUntilEOF(t *testing.T) {
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledDestinationConn{Conn: hostPipe, readStarted: make(chan struct{})}
	session, err := NewDestinationSession(hostConn)
	require.NoError(t, err)
	allowEOF := make(chan struct{})
	peerDone := make(chan error, 1)
	var allowEOFOnce sync.Once
	releaseEOF := func() { allowEOFOnce.Do(func() { close(allowEOF) }) }
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			releaseEOF()
			_ = session.Close()
			_ = agentConn.Close()
		})
	}
	defer stop()

	wantRequest := validDestinationSessionRequest()
	stdoutData := []byte{0, 0xff, 'o', 'u', 't'}
	stderrData := []byte{0, 0xfe, 'e', 'r', 'r'}
	go func() {
		gotRequest, peerErr := vmexecwire.DecodeDestinationRequest(context.Background(), agentConn)
		if peerErr == nil {
			if diff := cmp.Diff(wantRequest, gotRequest); diff != "" {
				peerErr = errors.New("destination request mismatch (-want +got):\n" + diff)
			}
		}
		var response *vmexecwire.ResponseWriter
		if peerErr == nil {
			response, peerErr = vmexecwire.NewResponseWriter(agentConn, gotRequest.RequestID)
		}
		if peerErr == nil {
			peerErr = response.WriteStdout(context.Background(), stdoutData)
		}
		if peerErr == nil {
			peerErr = response.WriteStderr(context.Background(), stderrData)
		}
		if peerErr == nil {
			peerErr = response.WriteResult(context.Background(), 7, 1250*time.Millisecond)
		}
		if peerErr == nil {
			<-allowEOF
		}
		closeErr := agentConn.Close()
		if peerErr == nil {
			peerErr = closeErr
		}
		peerDone <- peerErr
	}()

	ctx, cancel := context.WithTimeout(context.Background(), destinationSessionTestTimeout)
	defer cancel()
	require.NoError(t, session.Open(ctx, wantRequest))

	frame, err := session.Read(ctx)
	require.NoError(t, err)
	stdout, ok := frame.(vmexecwire.StdoutFrame)
	require.True(t, ok)
	if diff := cmp.Diff(stdoutData, stdout.Data); diff != "" {
		t.Errorf("stdout mismatch (-want +got):\n%s", diff)
	}

	frame, err = session.Read(ctx)
	require.NoError(t, err)
	stderr, ok := frame.(vmexecwire.StderrFrame)
	require.True(t, ok)
	if diff := cmp.Diff(stderrData, stderr.Data); diff != "" {
		t.Errorf("stderr mismatch (-want +got):\n%s", diff)
	}

	hostConn.signalAfterReads(3)
	terminalDone := make(chan destinationReadResult, 1)
	go func() {
		terminal, readErr := session.Read(ctx)
		terminalDone <- destinationReadResult{frame: terminal, err: readErr}
	}()
	select {
	case <-hostConn.readStarted:
	case <-time.After(destinationSessionTestTimeout):
		stop()
		t.Fatal("terminal read did not reach the physical EOF check")
	}
	select {
	case result := <-terminalDone:
		stop()
		t.Fatalf("terminal returned before physical EOF: frame=%T err=%v", result.frame, result.err)
	default:
	}
	releaseEOF()

	var terminalResult destinationReadResult
	select {
	case terminalResult = <-terminalDone:
	case <-time.After(destinationSessionTestTimeout):
		stop()
		t.Fatal("terminal read did not complete after physical EOF")
	}
	require.NoError(t, terminalResult.err)
	result, ok := terminalResult.frame.(vmexecwire.ResultFrame)
	require.True(t, ok)
	assert.Equal(t, 7, result.ExitCode)
	assert.Equal(t, 1250*time.Millisecond, result.Duration)

	frame, err = session.Read(ctx)
	assert.Nil(t, frame)
	assert.ErrorIs(t, err, io.EOF)
	select {
	case err = <-peerDone:
		assert.NoError(t, err)
	case <-time.After(destinationSessionTestTimeout):
		stop()
		t.Fatal("destination peer did not stop")
	}
}

func TestDestinationSessionWithholdsErrorTerminalUntilEOF(t *testing.T) {
	// Rationale: an error terminal is no more trustworthy than a result terminal until the closed grammar's physical
	// EOF proves that no trailing frame exists.
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledDestinationConn{Conn: hostPipe, readStarted: make(chan struct{})}
	session := openDestinationSession(t, hostConn, agentConn)
	allowEOF := make(chan struct{})
	var allowEOFOnce sync.Once
	releaseEOF := func() { allowEOFOnce.Do(func() { close(allowEOF) }) }
	defer releaseEOF()
	peerDone := make(chan error, 1)
	go func() {
		response, peerErr := vmexecwire.NewResponseWriter(agentConn, destinationSessionTestRequestID)
		if peerErr == nil {
			peerErr = response.WriteError(context.Background(), "destination rejected execution")
		}
		if peerErr == nil {
			<-allowEOF
		}
		closeErr := agentConn.Close()
		if peerErr == nil {
			peerErr = closeErr
		}
		peerDone <- peerErr
	}()

	hostConn.signalAfterReads(3)
	terminalDone := make(chan destinationReadResult, 1)
	go func() {
		frame, readErr := session.Read(t.Context())
		terminalDone <- destinationReadResult{frame: frame, err: readErr}
	}()
	select {
	case <-hostConn.readStarted:
	case <-time.After(destinationSessionTestTimeout):
		releaseEOF()
		_ = session.Close()
		t.Fatal("error terminal read did not reach the physical EOF check")
	}
	select {
	case result := <-terminalDone:
		releaseEOF()
		t.Fatalf("error terminal returned before physical EOF: frame=%T err=%v", result.frame, result.err)
	default:
	}
	releaseEOF()

	var terminalResult destinationReadResult
	select {
	case terminalResult = <-terminalDone:
	case <-time.After(destinationSessionTestTimeout):
		_ = session.Close()
		t.Fatal("error terminal read did not complete after physical EOF")
	}
	require.NoError(t, terminalResult.err)
	terminal, ok := terminalResult.frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, "destination rejected execution", terminal.Message)
	frame, err := session.Read(t.Context())
	assert.Nil(t, frame)
	assert.ErrorIs(t, err, io.EOF)
	awaitDestinationError(t, "destination error peer", peerDone)
}

func TestDestinationSessionOpenIsValidatedAndSingleAttempt(t *testing.T) {
	t.Run("nil connection", func(t *testing.T) {
		_, err := NewDestinationSession(nil)
		require.Error(t, err)
		assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)
	})

	t.Run("invalid request consumes the attempt and closes", func(t *testing.T) {
		// Rationale: retrying after validation failure could append a second request to an ambiguous transport.
		hostConn, agentConn := net.Pipe()
		session, err := NewDestinationSession(hostConn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = agentConn.Close() })

		request := validDestinationSessionRequest()
		request.AgentToken = "invalid"
		err = session.Open(t.Context(), request)
		require.Error(t, err)
		assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)
		_, peerErr := agentConn.Read(make([]byte, 1))
		assert.ErrorIs(t, peerErr, io.EOF)

		err = session.Open(t.Context(), validDestinationSessionRequest())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "single open attempt")
		assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)
	})

	t.Run("successful request cannot be reopened", func(t *testing.T) {
		hostConn, agentConn := net.Pipe()
		session, err := NewDestinationSession(hostConn)
		require.NoError(t, err)
		peerDone := make(chan error, 1)
		go func() {
			_, peerErr := vmexecwire.DecodeDestinationRequest(context.Background(), agentConn)
			if peerErr == nil {
				_, peerErr = agentConn.Read(make([]byte, 1))
				if errors.Is(peerErr, io.EOF) {
					peerErr = nil
				}
			}
			peerDone <- peerErr
		}()

		require.NoError(t, session.Open(t.Context(), validDestinationSessionRequest()))
		err = session.Open(t.Context(), validDestinationSessionRequest())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "single open attempt")
		assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)
		require.NoError(t, session.Close())
		awaitDestinationError(t, "destination peer", peerDone)
	})
}

func TestDestinationSessionLifetimeDeadlineClosesIdleTransport(t *testing.T) {
	// Rationale: callers can leave a successful session idle with a background context. The fixed execution deadline
	// must still release its transport without relying on a later Read, Cancel, or Close call.
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledDestinationConn{Conn: hostPipe}
	session, err := NewDestinationSession(hostConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	t.Cleanup(func() { _ = agentConn.Close() })
	fixedNow := time.Now()
	expired := make(chan time.Time)
	deadlineSeen := make(chan time.Time, 1)
	timerStopped := make(chan struct{})
	var stopTimerOnce sync.Once
	session.now = func() time.Time { return fixedNow }
	session.newLifetimeTimer = func(deadline time.Time) (<-chan time.Time, func()) {
		deadlineSeen <- deadline
		return expired, func() { stopTimerOnce.Do(func() { close(timerStopped) }) }
	}
	peerDone := make(chan error, 1)
	go func() {
		_, peerErr := vmexecwire.DecodeDestinationRequest(context.Background(), agentConn)
		if peerErr == nil {
			_, peerErr = agentConn.Read(make([]byte, 1))
			if errors.Is(peerErr, io.EOF) {
				peerErr = nil
			}
		}
		peerDone <- peerErr
	}()

	openErr := session.Open(context.Background(), validDestinationSessionRequest())
	if openErr != nil {
		_ = session.Close()
		_ = agentConn.Close()
		awaitDestinationError(t, "idle destination peer cleanup", peerDone)
		require.NoError(t, openErr)
	}
	select {
	case deadline := <-deadlineSeen:
		assert.True(t, deadline.Equal(fixedNow.Add(vmexecwire.MaxExecutionDuration)))
		_, writeDeadlines := hostConn.directionalDeadlines()
		require.Len(t, writeDeadlines, 1)
		assert.True(t, writeDeadlines[0].Equal(deadline))
	case <-time.After(destinationSessionTestTimeout):
		_ = session.Close()
		awaitDestinationError(t, "idle destination peer cleanup", peerDone)
		t.Fatal("destination lifetime timer was not armed")
	}
	close(expired)
	awaitDestinationError(t, "idle destination peer", peerDone)
	select {
	case <-timerStopped:
	case <-time.After(destinationSessionTestTimeout):
		t.Fatal("destination lifetime timer was not released")
	}
	assert.Equal(t, int32(1), hostConn.closeCalls.Load())
}

func TestDestinationSessionCanceledBlockedOpenClosesTransport(t *testing.T) {
	// Rationale: request framing uses multiple writes, so opening-context cancellation must interrupt a write blocked
	// before the destination reads even the first frame byte.
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledDestinationConn{Conn: hostPipe, writeStarted: make(chan struct{})}
	hostConn.observeWrites.Store(true)
	session, err := NewDestinationSession(hostConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	t.Cleanup(func() { _ = agentConn.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	openDone := make(chan error, 1)
	go func() { openDone <- session.Open(ctx, validDestinationSessionRequest()) }()
	select {
	case <-hostConn.writeStarted:
	case <-time.After(destinationSessionTestTimeout):
		cancel()
		_ = session.Close()
		select {
		case <-openDone:
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("blocked destination open did not stop after cleanup")
		}
		t.Fatal("destination open did not reach the blocked write")
	}
	cancel()
	var openErr error
	select {
	case openErr = <-openDone:
	case <-time.After(destinationSessionTestTimeout):
		_ = session.Close()
		select {
		case openErr = <-openDone:
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("blocked destination open did not stop after close")
		}
		t.Fatal("blocked destination open outlived cancellation")
	}
	require.Error(t, openErr)
	assert.ErrorIs(t, openErr, context.Canceled)
	assertDestinationSessionError(t, openErr, errs.CodeProcessError, errs.ClassInternal)
	assert.Equal(t, int32(1), hostConn.closeCalls.Load())
	_, err = agentConn.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

func TestDestinationSessionReadRequiresOpenAndOneSequentialReader(t *testing.T) {
	t.Run("before open", func(t *testing.T) {
		hostConn, agentConn := net.Pipe()
		session, err := NewDestinationSession(hostConn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = session.Close() })
		t.Cleanup(func() { _ = agentConn.Close() })

		frame, err := session.Read(t.Context())
		assert.Nil(t, frame)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not open")
		assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)
	})

	t.Run("concurrent reader", func(t *testing.T) {
		// Rationale: two readers could split one framed response and bypass its closed stream grammar.
		hostPipe, agentConn := net.Pipe()
		hostConn := &controlledDestinationConn{Conn: hostPipe, readStarted: make(chan struct{})}
		session := openDestinationSession(t, hostConn, agentConn)
		hostConn.signalAfterReads(1)
		firstRead := make(chan destinationReadResult, 1)
		go func() {
			frame, readErr := session.Read(t.Context())
			firstRead <- destinationReadResult{frame: frame, err: readErr}
		}()
		select {
		case <-hostConn.readStarted:
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("first reader did not reach the transport")
		}

		frame, err := session.Read(t.Context())
		assert.Nil(t, frame)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "single reader")
		assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)

		response, err := vmexecwire.NewResponseWriter(agentConn, destinationSessionTestRequestID)
		require.NoError(t, err)
		writeDone := make(chan error, 1)
		go func() { writeDone <- response.WriteStdout(t.Context(), []byte("released")) }()
		select {
		case result := <-firstRead:
			require.NoError(t, result.err)
			stdout, ok := result.frame.(vmexecwire.StdoutFrame)
			require.True(t, ok)
			if diff := cmp.Diff([]byte("released"), stdout.Data); diff != "" {
				t.Errorf("released stdout mismatch (-want +got):\n%s", diff)
			}
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("first reader did not complete")
		}
		awaitDestinationError(t, "response write", writeDone)
	})
}

func TestDestinationSessionReadRejectsInvalidResponseAndCloses(t *testing.T) {
	// Rationale: a destination protocol failure must make the one-request transport unusable before relay code can
	// mistake a partial or cross-request response for authorized output.
	tests := map[string]func(net.Conn) error{
		"EOF before terminal": func(net.Conn) error {
			return nil
		},
		"wrong request ID": func(conn net.Conn) error {
			response, err := vmexecwire.NewResponseWriter(conn, "ffeeddccbbaa99887766554433221100")
			if err != nil {
				return err
			}
			return response.WriteResult(context.Background(), 0, time.Millisecond)
		},
		"malformed frame magic": func(conn net.Conn) error {
			prefix := destinationSessionFramePrefix(1)
			prefix[0] = 'X'
			_, err := conn.Write(prefix)
			return err
		},
		"trailing data after terminal": func(conn net.Conn) error {
			response, err := vmexecwire.NewResponseWriter(conn, destinationSessionTestRequestID)
			if err != nil {
				return err
			}
			if err := response.WriteResult(context.Background(), 0, time.Millisecond); err != nil {
				return err
			}
			_, err = conn.Write([]byte{'x'})
			return err
		},
	}

	for name, writeResponse := range tests {
		t.Run(name, func(t *testing.T) {
			hostPipe, agentConn := net.Pipe()
			hostConn := &controlledDestinationConn{Conn: hostPipe}
			session := openDestinationSession(t, hostConn, agentConn)
			peerDone := make(chan error, 1)
			go func() {
				peerErr := writeResponse(agentConn)
				closeErr := agentConn.Close()
				if peerErr == nil {
					peerErr = closeErr
				}
				peerDone <- peerErr
			}()

			ctx, cancel := context.WithTimeout(context.Background(), destinationSessionTestTimeout)
			defer cancel()
			frame, err := session.Read(ctx)
			assert.Nil(t, frame)
			require.Error(t, err)
			assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)
			awaitDestinationError(t, "invalid response writer", peerDone)
			assert.Equal(t, int32(1), hostConn.closeCalls.Load())
		})
	}
}

func TestDestinationSessionCancelWritesExactRequestOnce(t *testing.T) {
	t.Run("before open", func(t *testing.T) {
		hostConn, agentConn := net.Pipe()
		session, err := NewDestinationSession(hostConn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = session.Close() })
		t.Cleanup(func() { _ = agentConn.Close() })

		err = session.Cancel(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not open")
		assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)
	})

	t.Run("exact identity and duplicate", func(t *testing.T) {
		// Rationale: cancellation is the only full-duplex control and must never target another request or be replayed.
		hostConn, agentConn := net.Pipe()
		session := openDestinationSession(t, hostConn, agentConn)
		peerDone := make(chan error, 1)
		go func() {
			peerErr := vmexecwire.DecodeCancel(
				context.Background(),
				agentConn,
				destinationSessionTestRequestID,
			)
			if peerErr == nil {
				_, peerErr = agentConn.Read(make([]byte, 1))
				if errors.Is(peerErr, io.EOF) {
					peerErr = nil
				}
			}
			peerDone <- peerErr
		}()

		require.NoError(t, session.Cancel(t.Context()))
		err := session.Cancel(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "single cancellation")
		assertDestinationSessionError(t, err, errs.CodeValidationFailed, errs.ClassValidation)
		require.NoError(t, session.Close())
		awaitDestinationError(t, "destination cancellation reader", peerDone)
	})
}

func TestDestinationSessionCanceledBlockedCancelClosesTransport(t *testing.T) {
	// Rationale: cancellation itself can block behind destination backpressure, so its operation context must remain
	// able to interrupt that write without disturbing a simultaneous reader's deadline machinery.
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledDestinationConn{Conn: hostPipe, writeStarted: make(chan struct{})}
	session := openDestinationSession(t, hostConn, agentConn)
	hostConn.observeWrites.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- session.Cancel(ctx) }()
	select {
	case <-hostConn.writeStarted:
	case <-time.After(destinationSessionTestTimeout):
		cancel()
		_ = session.Close()
		select {
		case <-cancelDone:
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("blocked destination cancellation did not stop after cleanup")
		}
		t.Fatal("destination cancellation did not reach the blocked write")
	}
	cancel()
	var cancelErr error
	select {
	case cancelErr = <-cancelDone:
	case <-time.After(destinationSessionTestTimeout):
		_ = session.Close()
		select {
		case cancelErr = <-cancelDone:
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("blocked destination cancellation did not stop after close")
		}
		t.Fatal("blocked destination cancellation outlived its operation context")
	}
	require.Error(t, cancelErr)
	assert.ErrorIs(t, cancelErr, context.Canceled)
	assertDestinationSessionError(t, cancelErr, errs.CodeProcessError, errs.ClassInternal)
	assert.Equal(t, int32(1), hostConn.closeCalls.Load())
	_, err := agentConn.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

func TestDestinationSessionDeadlineFailuresJoinCloseAsInternal(t *testing.T) {
	// Rationale: deadline and cleanup failures are system failures even when their raw causes do not carry a domain
	// class. Joined cleanup evidence must remain discoverable without weakening the returned classification.
	tests := map[string]struct {
		failRead bool
	}{
		"open write deadline": {},
		"response read deadline": {
			failRead: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			hostPipe, agentConn := net.Pipe()
			deadlineErr := errors.New("controlled destination deadline failure")
			closeErr := errors.New("controlled destination cleanup failure")
			hostConn := &controlledDestinationConn{Conn: hostPipe, closeErr: closeErr}
			var session *DestinationSession
			var err error
			if test.failRead {
				session = openDestinationSession(t, hostConn, agentConn)
				hostConn.readDeadlineErr = deadlineErr
				_, err = session.Read(t.Context())
			} else {
				hostConn.writeDeadlineErr = deadlineErr
				session, err = NewDestinationSession(hostConn)
				require.NoError(t, err)
				t.Cleanup(func() { _ = session.Close() })
				t.Cleanup(func() { _ = agentConn.Close() })
				err = session.Open(t.Context(), validDestinationSessionRequest())
			}

			require.Error(t, err)
			assert.ErrorIs(t, err, deadlineErr)
			assert.ErrorIs(t, err, closeErr)
			assertDestinationSessionError(t, err, errs.CodeProcessError, errs.ClassInternal)
			assert.Equal(t, int32(1), hostConn.closeCalls.Load())
			_, peerErr := agentConn.Read(make([]byte, 1))
			assert.ErrorIs(t, peerErr, io.EOF)
		})
	}
}

func TestDestinationSessionPartialReadStopsOnCancellationOrClose(t *testing.T) {
	// Rationale: a framed read can block after consuming a valid prefix fragment; both cancellation paths must close
	// the transport because deadlines alone cannot identify when an arbitrary context is canceled.
	tests := map[string]struct {
		closeSession bool
		wantCode     errs.Code
		wantClass    errs.Class
		canceled     bool
	}{
		"context cancellation": {
			wantCode:  errs.CodeProcessError,
			wantClass: errs.ClassInternal,
			canceled:  true,
		},
		"session close": {
			closeSession: true,
			wantCode:     errs.CodeValidationFailed,
			wantClass:    errs.ClassValidation,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			hostPipe, agentConn := net.Pipe()
			hostConn := &controlledDestinationConn{Conn: hostPipe, readStarted: make(chan struct{})}
			session := openDestinationSession(t, hostConn, agentConn)
			hostConn.signalAfterReads(1)
			readCtx, cancelRead := context.WithCancel(context.Background())
			defer cancelRead()
			readDone := make(chan destinationReadResult, 1)
			go func() {
				frame, readErr := session.Read(readCtx)
				readDone <- destinationReadResult{frame: frame, err: readErr}
			}()
			select {
			case <-hostConn.readStarted:
			case <-time.After(destinationSessionTestTimeout):
				_ = session.Close()
				select {
				case <-readDone:
				case <-time.After(destinationSessionTestTimeout):
					t.Fatal("partial response reader did not stop after cleanup")
				}
				t.Fatal("partial response reader did not reach the transport")
			}
			_, err := agentConn.Write([]byte("MVM"))
			require.NoError(t, err)
			if test.closeSession {
				require.NoError(t, session.Close())
			} else {
				cancelRead()
			}

			var result destinationReadResult
			select {
			case result = <-readDone:
			case <-time.After(destinationSessionTestTimeout):
				_ = session.Close()
				select {
				case result = <-readDone:
				case <-time.After(destinationSessionTestTimeout):
					t.Fatal("partial response read did not stop after cleanup")
				}
				t.Fatal("partial response read did not stop")
			}
			assert.Nil(t, result.frame)
			require.Error(t, result.err)
			assertDestinationSessionError(t, result.err, test.wantCode, test.wantClass)
			if test.canceled {
				assert.ErrorIs(t, result.err, context.Canceled)
			}
			_, err = agentConn.Read(make([]byte, 1))
			assert.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestDestinationSessionReadAndCancelUseIndependentFixedDeadlines(t *testing.T) {
	// Rationale: net.Conn permits one reader and one writer concurrently. A combined deadline update would couple
	// response backpressure to cancellation and could prevent the only control frame from reaching the agent.
	hostPipe, agentConn := net.Pipe()
	hostConn := &controlledDestinationConn{
		Conn:         hostPipe,
		readStarted:  make(chan struct{}),
		writeStarted: make(chan struct{}),
	}
	hostConn.rejectCombinedDeadline.Store(true)
	session, err := NewDestinationSession(hostConn)
	require.NoError(t, err)
	ctx, cancel := context.WithDeadline(
		context.Background(),
		time.Now().Add(2*destinationSessionTestTimeout),
	)
	fixedDeadline, ok := ctx.Deadline()
	require.True(t, ok)
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			_ = session.Close()
			_ = agentConn.Close()
		})
	}
	defer stop()

	openPeer := make(chan error, 1)
	go func() {
		_, peerErr := vmexecwire.DecodeDestinationRequest(ctx, agentConn)
		openPeer <- peerErr
	}()
	openErr := session.Open(ctx, validDestinationSessionRequest())
	if openErr != nil {
		stop()
		awaitDestinationError(t, "destination open reader", openPeer)
		require.NoError(t, openErr)
	}
	awaitDestinationError(t, "destination open reader", openPeer)

	hostConn.signalAfterReads(1)
	readDone := make(chan destinationReadResult, 1)
	go func() {
		frame, readErr := session.Read(ctx)
		readDone <- destinationReadResult{frame: frame, err: readErr}
	}()
	select {
	case <-hostConn.readStarted:
	case <-time.After(destinationSessionTestTimeout):
		stop()
		select {
		case <-readDone:
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("destination response read did not stop after cleanup")
		}
		t.Fatal("destination response read did not reach the transport")
	}

	hostConn.observeWrites.Store(true)
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- session.Cancel(ctx) }()
	select {
	case <-hostConn.writeStarted:
	case <-time.After(destinationSessionTestTimeout):
		stop()
		select {
		case <-readDone:
		case <-time.After(destinationSessionTestTimeout):
			t.Error("destination response read did not stop after cleanup")
		}
		select {
		case <-cancelDone:
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("destination cancellation did not stop after cleanup")
		}
		t.Fatal("destination cancellation write did not reach the transport")
	}

	peerDone := make(chan error, 1)
	go func() {
		peerErr := vmexecwire.DecodeCancel(ctx, agentConn, destinationSessionTestRequestID)
		var response *vmexecwire.ResponseWriter
		if peerErr == nil {
			response, peerErr = vmexecwire.NewResponseWriter(agentConn, destinationSessionTestRequestID)
		}
		if peerErr == nil {
			peerErr = response.WriteStdout(ctx, []byte("full-duplex"))
		}
		peerDone <- peerErr
	}()

	awaitDestinationError(t, "destination cancellation", cancelDone)
	var result destinationReadResult
	select {
	case result = <-readDone:
	case <-time.After(destinationSessionTestTimeout):
		stop()
		select {
		case result = <-readDone:
		case <-time.After(destinationSessionTestTimeout):
			t.Fatal("destination response read did not stop after cleanup")
		}
		awaitDestinationError(t, "destination full-duplex peer cleanup", peerDone)
		t.Fatal("destination response read did not complete")
	}
	require.NoError(t, result.err)
	stdout, ok := result.frame.(vmexecwire.StdoutFrame)
	require.True(t, ok)
	if diff := cmp.Diff([]byte("full-duplex"), stdout.Data); diff != "" {
		t.Errorf("full-duplex stdout mismatch (-want +got):\n%s", diff)
	}
	awaitDestinationError(t, "destination full-duplex peer", peerDone)

	readDeadlines, writeDeadlines := hostConn.directionalDeadlines()
	require.Len(t, readDeadlines, 1)
	require.Len(t, writeDeadlines, 2)
	assert.True(t, readDeadlines[0].Equal(fixedDeadline))
	assert.True(t, writeDeadlines[0].Equal(fixedDeadline))
	assert.True(t, writeDeadlines[1].Equal(fixedDeadline))
}

func TestDestinationSessionCloseRunsOnceAndCachesDomainError(t *testing.T) {
	hostPipe, agentConn := net.Pipe()
	closeErr := errors.New("controlled destination close failure")
	hostConn := &controlledDestinationConn{Conn: hostPipe, closeErr: closeErr}
	session, err := NewDestinationSession(hostConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = agentConn.Close() })

	firstErr := session.Close()
	require.Error(t, firstErr)
	assert.ErrorIs(t, firstErr, closeErr)
	assertDestinationSessionError(t, firstErr, errs.CodeProcessError, errs.ClassInternal)
	secondErr := session.Close()
	require.Error(t, secondErr)
	assert.ErrorIs(t, secondErr, closeErr)
	assertDestinationSessionError(t, secondErr, errs.CodeProcessError, errs.ClassInternal)
	assert.Equal(t, int32(1), hostConn.closeCalls.Load())
	_, err = agentConn.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

func validDestinationSessionRequest() vmexecwire.DestinationRequest {
	return vmexecwire.DestinationRequest{
		RequestID:  destinationSessionTestRequestID,
		AgentToken: destinationSessionTestToken,
		TargetUser: "builder",
		Command:    "printf ready",
	}
}

func destinationSessionFramePrefix(bodyLength int) []byte {
	prefix := make([]byte, len(vmexecwire.FrameMagic)+2+4)
	copy(prefix, vmexecwire.FrameMagic)
	binary.BigEndian.PutUint16(prefix[len(vmexecwire.FrameMagic):], 1)
	binary.BigEndian.PutUint32(prefix[len(vmexecwire.FrameMagic)+2:], uint32(bodyLength))
	return prefix
}

// openDestinationSession sends and verifies one request before returning the active session.
func openDestinationSession(t *testing.T, hostConn net.Conn, agentConn net.Conn) *DestinationSession {
	t.Helper()
	session, err := NewDestinationSession(hostConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	t.Cleanup(func() { _ = agentConn.Close() })
	peerDone := make(chan error, 1)
	go func() {
		request, peerErr := vmexecwire.DecodeDestinationRequest(t.Context(), agentConn)
		if peerErr == nil {
			if diff := cmp.Diff(validDestinationSessionRequest(), request); diff != "" {
				peerErr = errors.New("destination request mismatch (-want +got):\n" + diff)
			}
		}
		peerDone <- peerErr
	}()
	openErr := session.Open(t.Context(), validDestinationSessionRequest())
	if openErr != nil {
		_ = session.Close()
		_ = agentConn.Close()
		awaitDestinationError(t, "destination open", peerDone)
		require.NoError(t, openErr)
	}
	awaitDestinationError(t, "destination open", peerDone)
	return session
}

func assertDestinationSessionError(t *testing.T, err error, code errs.Code, class errs.Class) {
	t.Helper()
	var domainErr *errs.DomainError
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, code, domainErr.Code)
	assert.Equal(t, class, domainErr.Class)
}

func awaitDestinationError(t *testing.T, name string, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		assert.NoError(t, err, name+" failed")
	case <-time.After(destinationSessionTestTimeout):
		t.Errorf("%s did not complete", name)
	}
}

type destinationReadResult struct {
	frame vmexecwire.ResponseFrame
	err   error
}

type controlledDestinationConn struct {
	net.Conn
	readCountdown          atomic.Int32
	readOnce               sync.Once
	writeOnce              sync.Once
	readStarted            chan struct{}
	writeStarted           chan struct{}
	observeWrites          atomic.Bool
	rejectCombinedDeadline atomic.Bool
	closeCalls             atomic.Int32
	closeErr               error
	deadlineMu             sync.Mutex
	readDeadlines          []time.Time
	writeDeadlines         []time.Time
	readDeadlineErr        error
	writeDeadlineErr       error
}

func (conn *controlledDestinationConn) signalAfterReads(reads int32) {
	conn.readCountdown.Store(reads)
}

func (conn *controlledDestinationConn) Read(value []byte) (int, error) {
	if remaining := conn.readCountdown.Load(); remaining > 0 && conn.readCountdown.Add(-1) == 0 {
		conn.readOnce.Do(func() { close(conn.readStarted) })
	}
	return conn.Conn.Read(value)
}

func (conn *controlledDestinationConn) Write(value []byte) (int, error) {
	if conn.observeWrites.Load() {
		conn.writeOnce.Do(func() { close(conn.writeStarted) })
	}
	return conn.Conn.Write(value)
}

func (conn *controlledDestinationConn) SetDeadline(deadline time.Time) error {
	if conn.rejectCombinedDeadline.Load() {
		return errCombinedDestinationDeadline
	}
	return conn.Conn.SetDeadline(deadline)
}

func (conn *controlledDestinationConn) SetReadDeadline(deadline time.Time) error {
	conn.deadlineMu.Lock()
	conn.readDeadlines = append(conn.readDeadlines, deadline)
	conn.deadlineMu.Unlock()
	if conn.readDeadlineErr != nil {
		return conn.readDeadlineErr
	}
	return conn.Conn.SetReadDeadline(deadline)
}

func (conn *controlledDestinationConn) SetWriteDeadline(deadline time.Time) error {
	conn.deadlineMu.Lock()
	conn.writeDeadlines = append(conn.writeDeadlines, deadline)
	conn.deadlineMu.Unlock()
	if conn.writeDeadlineErr != nil {
		return conn.writeDeadlineErr
	}
	return conn.Conn.SetWriteDeadline(deadline)
}

func (conn *controlledDestinationConn) directionalDeadlines() ([]time.Time, []time.Time) {
	conn.deadlineMu.Lock()
	defer conn.deadlineMu.Unlock()
	return append([]time.Time(nil), conn.readDeadlines...), append([]time.Time(nil), conn.writeDeadlines...)
}

func (conn *controlledDestinationConn) Close() error {
	conn.closeCalls.Add(1)
	underlyingErr := conn.Conn.Close()
	if conn.closeErr != nil {
		return conn.closeErr
	}
	return underlyingErr
}

var errCombinedDestinationDeadline = errors.New("combined destination deadline would couple full-duplex operations")
