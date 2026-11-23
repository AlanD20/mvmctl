package vmexecwire_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

func TestResponseStreamRoundTripHasOneTerminalAndEOF(t *testing.T) {
	var encoded bytes.Buffer
	writer, err := vmexecwire.NewResponseWriter(&encoded, testRequestID)
	require.NoError(t, err)
	stdoutData := []byte{0, 0xff, 'r', 'e', 'a', 'd', 'y', '\n'}
	require.NoError(t, writer.WriteStdout(t.Context(), stdoutData))
	require.NoError(t, writer.WriteStderr(t.Context(), []byte("warning\n")))
	require.NoError(t, writer.WriteResult(t.Context(), 7, 1250*time.Millisecond))

	reader, err := vmexecwire.NewResponseReader(bytes.NewReader(encoded.Bytes()), testRequestID)
	require.NoError(t, err)

	frame, err := reader.Read(t.Context())
	require.NoError(t, err)
	stdout, ok := frame.(vmexecwire.StdoutFrame)
	require.True(t, ok)
	assert.Equal(t, stdoutData, stdout.Data)

	frame, err = reader.Read(t.Context())
	require.NoError(t, err)
	stderr, ok := frame.(vmexecwire.StderrFrame)
	require.True(t, ok)
	assert.Equal(t, []byte("warning\n"), stderr.Data)

	frame, err = reader.Read(t.Context())
	require.NoError(t, err)
	result, ok := frame.(vmexecwire.ResultFrame)
	require.True(t, ok)
	assert.Equal(t, 7, result.ExitCode)
	assert.Equal(t, 1250*time.Millisecond, result.Duration)

	frame, err = reader.Read(t.Context())
	assert.Nil(t, frame)
	assert.ErrorIs(t, err, io.EOF)
}

func TestResponseStreamErrorIsTerminal(t *testing.T) {
	var encoded bytes.Buffer
	writer, err := vmexecwire.NewResponseWriter(&encoded, testRequestID)
	require.NoError(t, err)
	require.NoError(t, writer.WriteError(t.Context(), "destination execution failed"))

	reader, err := vmexecwire.NewResponseReader(bytes.NewReader(encoded.Bytes()), testRequestID)
	require.NoError(t, err)
	frame, err := reader.Read(t.Context())
	require.NoError(t, err)
	terminal, ok := frame.(vmexecwire.ErrorFrame)
	require.True(t, ok)
	assert.Equal(t, "destination execution failed", terminal.Message)
	_, err = reader.Read(t.Context())
	assert.ErrorIs(t, err, io.EOF)
}

func TestResponseWriterRejectsInvalidTransitionsAndBounds(t *testing.T) {
	tests := map[string]func(*vmexecwire.ResponseWriter) error{
		"empty stdout": func(writer *vmexecwire.ResponseWriter) error {
			return writer.WriteStdout(t.Context(), nil)
		},
		"stdout exceeds one frame": func(writer *vmexecwire.ResponseWriter) error {
			return writer.WriteStdout(t.Context(), bytes.Repeat([]byte{'x'}, vmexecwire.MaxFrameBytes))
		},
		"invalid exit code": func(writer *vmexecwire.ResponseWriter) error {
			return writer.WriteResult(t.Context(), 256, time.Second)
		},
		"negative duration": func(writer *vmexecwire.ResponseWriter) error {
			return writer.WriteResult(t.Context(), 0, -time.Second)
		},
		"duration over limit": func(writer *vmexecwire.ResponseWriter) error {
			return writer.WriteResult(t.Context(), 0, vmexecwire.MaxExecutionDuration+time.Millisecond)
		},
		"empty error": func(writer *vmexecwire.ResponseWriter) error {
			return writer.WriteError(t.Context(), "")
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			writer, err := vmexecwire.NewResponseWriter(io.Discard, testRequestID)
			require.NoError(t, err)
			assert.Error(t, run(writer))
		})
	}

	writer, err := vmexecwire.NewResponseWriter(io.Discard, testRequestID)
	require.NoError(t, err)
	require.NoError(t, writer.WriteResult(t.Context(), 0, time.Second))
	assert.Error(t, writer.WriteResult(t.Context(), 0, time.Second), "duplicate terminal must fail")
	assert.Error(t, writer.WriteStdout(t.Context(), []byte("late")), "post-terminal output must fail")
}

func TestResponseReaderRejectsInvalidStreamGrammar(t *testing.T) {
	validResult := `{"schema_version":1,"request_id":"` + testRequestID +
		`","type":"result","exit_code":0,"duration_ms":1}`
	validStdout := `{"schema_version":1,"request_id":"` + testRequestID +
		`","type":"stdout","data":"` + base64.StdEncoding.EncodeToString([]byte("ok")) + `"}`
	tests := map[string]struct {
		stream  []byte
		reads   int
		wantErr string
	}{
		"EOF before terminal": {stream: nil, reads: 1, wantErr: "before terminal"},
		"unknown type": {
			stream:  streamFrameForTest(strings.Replace(validStdout, `"stdout"`, `"progress"`, 1)),
			reads:   1,
			wantErr: "type",
		},
		"mismatched request ID": {
			stream:  streamFrameForTest(strings.Replace(validResult, testRequestID, strings.Repeat("a", 32), 1)),
			reads:   1,
			wantErr: "request ID",
		},
		"cross-type field": {
			stream: streamFrameForTest(strings.Replace(
				validStdout[:len(validStdout)-1], `"data":"b2s="`, `"data":"b2s=","exit_code":0`, 1,
			) + `}`),
			reads:   1,
			wantErr: "unknown",
		},
		"cancel in response stream": {
			stream:  streamFrameForTest(`{"schema_version":1,"request_id":"` + testRequestID + `","type":"cancel"}`),
			reads:   1,
			wantErr: "cancel",
		},
		"post-terminal output": {
			stream:  append(streamFrameForTest(validResult), streamFrameForTest(validStdout)...),
			reads:   2,
			wantErr: "after terminal",
		},
		"duplicate terminal": {
			stream:  append(streamFrameForTest(validResult), streamFrameForTest(validResult)...),
			reads:   2,
			wantErr: "after terminal",
		},
		"duplicate field": {
			stream: streamFrameForTest(`{"schema_version":1,"request_id":"` + testRequestID +
				`","type":"stdout","type":"stdout","data":"b2s="}`),
			reads:   1,
			wantErr: "duplicate",
		},
		"case-colliding field": {
			stream: streamFrameForTest(`{"schema_version":1,"request_id":"` + testRequestID +
				`","type":"stdout","Type":"stdout","data":"b2s="}`),
			reads:   1,
			wantErr: "case",
		},
		"trailing JSON": {
			stream:  streamFrameForTest(validStdout + `{}`),
			reads:   1,
			wantErr: "trailing",
		},
		"invalid UTF-8": {
			stream:  requestFrameForTest(string([]byte{'{', 0xff, '}'})),
			reads:   1,
			wantErr: "UTF-8",
		},
		"overflowing duration": {
			stream: streamFrameForTest(`{"schema_version":1,"request_id":"` + testRequestID +
				`","type":"result","exit_code":0,"duration_ms":9223372036854775807}`),
			reads:   1,
			wantErr: "duration",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			reader, err := vmexecwire.NewResponseReader(bytes.NewReader(test.stream), testRequestID)
			require.NoError(t, err)
			for range test.reads - 1 {
				_, err = reader.Read(t.Context())
				require.NoError(t, err)
			}
			_, err = reader.Read(t.Context())
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestResponseReaderEnforcesCombinedOutputLimit(t *testing.T) {
	var encoded bytes.Buffer
	writer, err := vmexecwire.NewResponseWriter(&encoded, testRequestID)
	require.NoError(t, err)
	chunk := bytes.Repeat([]byte{'a'}, 32*1024)
	for written := 0; written < vmexecwire.MaxOutputBytes; written += len(chunk) {
		require.NoError(t, writer.WriteStdout(t.Context(), chunk))
	}
	assert.Error(t, writer.WriteStderr(t.Context(), []byte{'x'}))

	encoded.Write(streamFrameForTest(`{"schema_version":1,"request_id":"` + testRequestID +
		`","type":"stderr","data":"eA=="}`))
	reader, err := vmexecwire.NewResponseReader(bytes.NewReader(encoded.Bytes()), testRequestID)
	require.NoError(t, err)
	for range vmexecwire.MaxOutputBytes / len(chunk) {
		_, err = reader.Read(t.Context())
		require.NoError(t, err)
	}
	_, err = reader.Read(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output")
}

func TestCancelFrameRoundTripAndIdentity(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, vmexecwire.WriteCancel(t.Context(), &encoded, testRequestID))
	require.NoError(t, vmexecwire.DecodeCancel(t.Context(), bytes.NewReader(encoded.Bytes()), testRequestID))

	err := vmexecwire.DecodeCancel(
		t.Context(),
		bytes.NewReader(encoded.Bytes()),
		strings.Repeat("a", 32),
	)
	assert.Error(t, err)

	for name, body := range map[string]string{
		"non-cancel type": `{"schema_version":1,"request_id":"` + testRequestID + `","type":"result"}`,
		"unknown field": `{"schema_version":1,"request_id":"` + testRequestID +
			`","type":"cancel","command":"id"}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := vmexecwire.DecodeCancel(t.Context(), bytes.NewReader(streamFrameForTest(body)), testRequestID)
			require.Error(t, err)
		})
	}
}

func TestResponseIOHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer, err := vmexecwire.NewResponseWriter(io.Discard, testRequestID)
	require.NoError(t, err)
	assert.ErrorIs(t, writer.WriteStdout(ctx, []byte("data")), context.Canceled)

	reader, err := vmexecwire.NewResponseReader(bytes.NewReader(nil), testRequestID)
	require.NoError(t, err)
	_, err = reader.Read(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestResponseConstructorsRejectInvalidDependenciesAndIdentity(t *testing.T) {
	_, err := vmexecwire.NewResponseWriter(nil, testRequestID)
	assert.Error(t, err)
	_, err = vmexecwire.NewResponseReader(nil, testRequestID)
	assert.Error(t, err)
	_, err = vmexecwire.NewResponseWriter(io.Discard, "not-an-id")
	assert.Error(t, err)
	_, err = vmexecwire.NewResponseReader(bytes.NewReader(nil), "not-an-id")
	assert.Error(t, err)
}

func streamFrameForTest(body string) []byte {
	return requestFrameForTest(body)
}

func TestResponseReaderReturnsProtocolErrorsAsDomainErrors(t *testing.T) {
	reader, err := vmexecwire.NewResponseReader(bytes.NewReader(nil), testRequestID)
	require.NoError(t, err)
	_, err = reader.Read(t.Context())
	var domainErr *errs.DomainError
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
}
