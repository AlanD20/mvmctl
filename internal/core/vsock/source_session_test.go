package vsock

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
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
