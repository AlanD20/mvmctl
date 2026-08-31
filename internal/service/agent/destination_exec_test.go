package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
)

const (
	destinationTestRequestID = "0123456789abcdef0123456789abcdef"
	destinationTestToken     = "01234567-89ab-4def-8123-456789abcdef"
	destinationTestTimeout   = 3 * time.Second
)

type fakeDestinationExecutionEffect struct {
	mu      sync.Mutex
	calls   int
	execute func(context.Context, string, string, io.Writer, io.Writer) (int, error)
}

func (effect *fakeDestinationExecutionEffect) Execute(
	ctx context.Context,
	targetUser string,
	command string,
	stdout io.Writer,
	stderr io.Writer,
) (int, error) {
	effect.mu.Lock()
	effect.calls++
	effect.mu.Unlock()
	return effect.execute(ctx, targetUser, command, stdout, stderr)
}

func (effect *fakeDestinationExecutionEffect) callCount() int {
	effect.mu.Lock()
	defer effect.mu.Unlock()
	return effect.calls
}

func TestHandleConnectionRoutesDestinationAndLegacyProtocols(t *testing.T) {
	t.Run("exact VM-exec magic routes to destination execution", func(t *testing.T) {
		effect := &fakeDestinationExecutionEffect{
			execute: func(_ context.Context, user, command string, stdout, stderr io.Writer) (int, error) {
				assert.Equal(t, "builder", user)
				assert.Equal(t, "private command", command)
				_, err := stdout.Write([]byte{0, 0xff, 'o', 'k'})
				if err != nil {
					return -1, err
				}
				_, err = stderr.Write([]byte{0, 0xfe, 'w', 'a', 'r', 'n'})
				return 7, err
			},
		}
		agent := New(9999, destinationTestToken, "")
		agent.destinationExecution = effect
		client, server := net.Pipe()
		require.NoError(t, client.SetDeadline(time.Now().Add(destinationTestTimeout)))
		done := make(chan struct{})
		go func() {
			defer close(done)
			agent.handleConnection(t.Context(), server)
		}()

		request := vmexecwire.DestinationRequest{
			RequestID:  destinationTestRequestID,
			AgentToken: destinationTestToken,
			TargetUser: "builder",
			Command:    "private command",
		}
		require.NoError(t, vmexecwire.EncodeDestinationRequest(t.Context(), client, request))
		reader, err := vmexecwire.NewResponseReader(client, destinationTestRequestID)
		require.NoError(t, err)
		frame, err := reader.Read(t.Context())
		require.NoError(t, err)
		stdout, ok := frame.(vmexecwire.StdoutFrame)
		require.True(t, ok)
		assert.Equal(t, []byte{0, 0xff, 'o', 'k'}, stdout.Data)
		frame, err = reader.Read(t.Context())
		require.NoError(t, err)
		stderr, ok := frame.(vmexecwire.StderrFrame)
		require.True(t, ok)
		assert.Equal(t, []byte{0, 0xfe, 'w', 'a', 'r', 'n'}, stderr.Data)
		frame, err = reader.Read(t.Context())
		require.NoError(t, err)
		result, ok := frame.(vmexecwire.ResultFrame)
		require.True(t, ok)
		assert.Equal(t, 7, result.ExitCode)
		_, err = reader.Read(t.Context())
		assert.ErrorIs(t, err, io.EOF)
		assert.Equal(t, 1, effect.callCount())
		waitForDestinationHandler(t, done)
	})

	t.Run("legacy BOM and whitespace route to the existing JSON loop", func(t *testing.T) {
		effect := &fakeDestinationExecutionEffect{
			execute: func(context.Context, string, string, io.Writer, io.Writer) (int, error) {
				return 0, nil
			},
		}
		agent := New(9999, destinationTestToken, "")
		agent.destinationExecution = effect
		client, server := net.Pipe()
		require.NoError(t, client.SetDeadline(time.Now().Add(destinationTestTimeout)))
		done := make(chan struct{})
		go func() {
			defer close(done)
			agent.handleConnection(t.Context(), server)
		}()

		request, err := json.Marshal(execRequest{ID: "legacy-ping", Type: requestTypePing})
		require.NoError(t, err)
		request = append([]byte{0xef, 0xbb, 0xbf, ' ', '\t'}, request...)
		request = append(request, '\n')
		_, err = client.Write(request)
		require.NoError(t, err)
		var response execResponse
		require.NoError(t, json.NewDecoder(client).Decode(&response))
		assert.Equal(t, "legacy-ping", response.ID)
		assert.Equal(t, responseTypePong, response.Type)
		assert.Equal(t, 0, effect.callCount())

		require.NoError(t, client.Close())
		waitForDestinationHandler(t, done)
	})

	t.Run("legacy file transfer reaches the binary handoff", func(t *testing.T) {
		agent := New(9999, destinationTestToken, "")
		client, server := net.Pipe()
		require.NoError(t, client.SetDeadline(time.Now().Add(destinationTestTimeout)))
		done := make(chan struct{})
		go func() {
			defer close(done)
			agent.handleConnection(t.Context(), server)
		}()

		request, err := json.Marshal(execRequest{
			ID: "legacy-file-transfer", Type: requestTypeFileTransfer, Token: destinationTestToken,
		})
		require.NoError(t, err)
		request = append(request, '\n')
		_, err = client.Write(request)
		require.NoError(t, err)
		var response execResponse
		require.NoError(t, json.NewDecoder(client).Decode(&response))
		assert.Equal(t, responseTypeFTReady, response.Type)

		require.NoError(t, WriteFTFrame(client, 0xff, nil))
		frameType, payload, err := ReadFTFrame(client)
		require.NoError(t, err)
		assert.Equal(t, FtError, frameType)
		var transferError FtErrorPayload
		require.NoError(t, json.Unmarshal(payload, &transferError))
		assert.Equal(t, "invalid", transferError.Code)
		waitForDestinationHandler(t, done)
	})
}

func TestDestinationAuthenticationFailsBeforeExecution(t *testing.T) {
	for name, configuredToken := range map[string]string{
		"empty configured token": "",
		"wrong configured token": "fedcba98-7654-4cba-8765-43210fedcba9",
	} {
		t.Run(name, func(t *testing.T) {
			effect := &fakeDestinationExecutionEffect{
				execute: func(context.Context, string, string, io.Writer, io.Writer) (int, error) {
					t.Fatal("execution reached before authentication")
					return -1, nil
				},
			}
			frame := runDestinationRequest(t, configuredToken, effect, destinationTestToken, nil)
			terminal, ok := frame.(vmexecwire.ErrorFrame)
			require.True(t, ok)
			assert.Equal(t, destinationUnauthorizedMessage, terminal.Message)
			assert.NotContains(t, terminal.Message, destinationTestToken)
			assert.Equal(t, 0, effect.callCount())
		})
	}
}

func TestDestinationExecutionFailureRedactsCommandTokenAndCause(t *testing.T) {
	secretCommand := "printf super-secret-command"
	secretCause := "super-secret-command " + destinationTestToken
	effect := &fakeDestinationExecutionEffect{
		execute: func(context.Context, string, string, io.Writer, io.Writer) (int, error) {
			return -1, errors.New(secretCause)
		},
	}
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	frame := runDestinationRequest(
		t,
		destinationTestToken,
		effect,
		destinationTestToken,
		func(request *vmexecwire.DestinationRequest) {
			request.Command = secretCommand
		},
	)
	terminal, ok := frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, destinationExecutionErrorMessage, terminal.Message)
	assert.NotContains(t, terminal.Message, secretCommand)
	assert.NotContains(t, terminal.Message, destinationTestToken)
	assert.NotContains(t, logs.String(), secretCommand)
	assert.NotContains(t, logs.String(), destinationTestToken)
}

func TestDestinationExecutionRejectsOversizedOutputWrite(t *testing.T) {
	effect := &fakeDestinationExecutionEffect{
		execute: func(_ context.Context, _, _ string, stdout, _ io.Writer) (int, error) {
			_, _ = stdout.Write(bytes.Repeat([]byte{'x'}, vmexecwire.MaxOutputBytes+1))
			return 0, nil
		},
	}
	frame := runDestinationRequest(t, destinationTestToken, effect, destinationTestToken, nil)
	terminal, ok := frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, destinationExecutionErrorMessage, terminal.Message)
}

func TestDestinationExecutionUsesFixedDeadline(t *testing.T) {
	effect := &fakeDestinationExecutionEffect{
		execute: func(ctx context.Context, _, _ string, _, _ io.Writer) (int, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			remaining := time.Until(deadline)
			assert.Greater(t, remaining, vmexecwire.MaxExecutionDuration-time.Second)
			assert.LessOrEqual(t, remaining, vmexecwire.MaxExecutionDuration)
			return 0, nil
		},
	}
	frame := runDestinationRequest(t, destinationTestToken, effect, destinationTestToken, nil)
	_, ok := frame.(vmexecwire.ResultFrame)
	assert.True(t, ok)
}

func TestDestinationExecutionHonorsParentTimeout(t *testing.T) {
	effect := &fakeDestinationExecutionEffect{
		execute: func(ctx context.Context, _, _ string, _, _ io.Writer) (int, error) {
			<-ctx.Done()
			return -1, ctx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	frame := runDestinationRequestWithContext(t, ctx, destinationTestToken, effect, destinationTestToken, nil, nil)
	terminal, ok := frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, destinationTimeoutMessage, terminal.Message)
}

func TestDestinationExecutionHonorsMatchingCancel(t *testing.T) {
	effect := &fakeDestinationExecutionEffect{
		execute: func(ctx context.Context, _, _ string, _, _ io.Writer) (int, error) {
			<-ctx.Done()
			return -1, ctx.Err()
		},
	}
	frame := runDestinationRequestWithContext(
		t,
		t.Context(),
		destinationTestToken,
		effect,
		destinationTestToken,
		nil,
		func(client net.Conn) {
			require.NoError(t, vmexecwire.WriteCancel(t.Context(), client, destinationTestRequestID))
		},
	)
	terminal, ok := frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, destinationCanceledMessage, terminal.Message)
}

func TestDestinationExecutionRejectsMismatchedCancel(t *testing.T) {
	effect := &fakeDestinationExecutionEffect{
		execute: func(ctx context.Context, _, _ string, _, _ io.Writer) (int, error) {
			<-ctx.Done()
			return -1, ctx.Err()
		},
	}
	frame := runDestinationRequestWithContext(
		t,
		t.Context(),
		destinationTestToken,
		effect,
		destinationTestToken,
		nil,
		func(client net.Conn) {
			require.NoError(t, vmexecwire.WriteCancel(t.Context(), client, strings.Repeat("a", 32)))
		},
	)
	terminal, ok := frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, destinationCancelErrorMessage, terminal.Message)
}

func TestDestinationExecutionRejectsMalformedCancel(t *testing.T) {
	effect := &fakeDestinationExecutionEffect{
		execute: func(ctx context.Context, _, _ string, _, _ io.Writer) (int, error) {
			<-ctx.Done()
			return -1, ctx.Err()
		},
	}
	frame := runDestinationRequestWithContext(
		t,
		t.Context(),
		destinationTestToken,
		effect,
		destinationTestToken,
		nil,
		func(client net.Conn) {
			writeVMExecTestFrame(t, client, `{"schema_version":1,"request_id":"`+
				destinationTestRequestID+`","type":"result","exit_code":0,"duration_ms":0}`)
		},
	)
	terminal, ok := frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, destinationCancelErrorMessage, terminal.Message)
}

func runDestinationRequest(
	t *testing.T,
	configuredToken string,
	effect destinationExecutionEffect,
	requestToken string,
	mutate func(*vmexecwire.DestinationRequest),
) vmexecwire.ResponseFrame {
	t.Helper()
	return runDestinationRequestWithContext(t, t.Context(), configuredToken, effect, requestToken, mutate, nil)
}

func runDestinationRequestWithContext(
	t *testing.T,
	ctx context.Context,
	configuredToken string,
	effect destinationExecutionEffect,
	requestToken string,
	mutate func(*vmexecwire.DestinationRequest),
	afterRequest func(net.Conn),
) vmexecwire.ResponseFrame {
	t.Helper()
	agent := New(9999, configuredToken, "")
	agent.destinationExecution = effect
	client, server := net.Pipe()
	require.NoError(t, client.SetDeadline(time.Now().Add(destinationTestTimeout)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.handleConnection(ctx, server)
	}()
	request := vmexecwire.DestinationRequest{
		RequestID:  destinationTestRequestID,
		AgentToken: requestToken,
		TargetUser: "builder",
		Command:    "printf ready",
	}
	if mutate != nil {
		mutate(&request)
	}
	require.NoError(t, vmexecwire.EncodeDestinationRequest(t.Context(), client, request))
	if afterRequest != nil {
		afterRequest(client)
	}
	reader, err := vmexecwire.NewResponseReader(client, destinationTestRequestID)
	require.NoError(t, err)
	frame, err := reader.Read(t.Context())
	require.NoError(t, err)
	_, err = reader.Read(t.Context())
	require.ErrorIs(t, err, io.EOF)
	waitForDestinationHandler(t, done)
	return frame
}

func writeVMExecTestFrame(t *testing.T, writer io.Writer, body string) {
	t.Helper()
	prefix := make([]byte, len(vmexecwire.FrameMagic)+2+4)
	copy(prefix, vmexecwire.FrameMagic)
	binary.BigEndian.PutUint16(prefix[len(vmexecwire.FrameMagic):], 1)
	binary.BigEndian.PutUint32(prefix[len(vmexecwire.FrameMagic)+2:], uint32(len(body)))
	_, err := writer.Write(append(prefix, body...))
	require.NoError(t, err)
}

func waitForDestinationHandler(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(destinationTestTimeout):
		t.Fatal("destination handler did not stop before the test deadline")
	}
}
