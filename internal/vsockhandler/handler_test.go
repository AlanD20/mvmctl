// Package vsockhandler_test verifies guest-originated host-frame dispatch.
package vsockhandler_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/service/agent"
	"mvmctl/internal/vsockhandler"
	"mvmctl/pkg/errs"
)

const remoteExecDeniedMessage = "remote VM execution is not authorized"

type recordingConn struct {
	bytes.Buffer
}

func (c *recordingConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *recordingConn) Close() error                       { return nil }
func (c *recordingConn) LocalAddr() net.Addr                { return recordingAddr("source") }
func (c *recordingConn) RemoteAddr() net.Addr               { return recordingAddr("guest") }
func (c *recordingConn) SetDeadline(_ time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(_ time.Time) error { return nil }

type recordingAddr string

func (a recordingAddr) Network() string { return "recording" }
func (a recordingAddr) String() string  { return string(a) }

func decodeRemoteVMResponses(t *testing.T, conn *recordingConn) []agent.RemoteVMResponse {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(conn.Bytes()))
	var responses []agent.RemoteVMResponse
	for {
		var response agent.RemoteVMResponse
		err := decoder.Decode(&response)
		if err == io.EOF {
			return responses
		}
		require.NoError(t, err)
		responses = append(responses, response)
	}
}

func TestHandleUnknownFrameTypeReturnsNil(t *testing.T) {
	handler := &vsockhandler.Handler{}
	conn := &recordingConn{}

	err := handler.Handle(t.Context(), "source-vm", conn, "bogus_type", "opaque")

	assert.NoError(t, err)
	assert.Zero(t, conn.Len(), "unknown frame types must not write a response")
}

func TestHandleRemoteVMLegacyRequestDeniedWithoutEffects(t *testing.T) {
	// Rationale: The legacy all-to-all remote-exec path bypasses typed exec
	// policy. Denial must happen before inspecting identities or command data.
	tests := map[string]string{
		"authorized-looking request": `{"destination":"target-vm","command":"DO-NOT-LOG-THIS"}`,
		"malformed request":          `{"command":`,
		"empty request":              "",
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			originalLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(originalLogger)

			handler := &vsockhandler.Handler{}
			conn := &recordingConn{}

			err := handler.Handle(
				t.Context(),
				"source-vm-secret",
				conn,
				vsock.ResponseTypeRemoteVM,
				data,
			)

			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeUnauthorized, domainErr.Code)

			responses := decodeRemoteVMResponses(t, conn)
			want := []agent.RemoteVMResponse{{
				Type:   vsock.ResponseTypeRemoteVM,
				Status: 1,
				Error:  remoteExecDeniedMessage,
			}}
			if diff := cmp.Diff(want, responses); diff != "" {
				t.Errorf("terminal responses mismatch (-want +got):\n%s", diff)
			}

			assert.NotContains(t, logs.String(), "source-vm-secret")
			assert.NotContains(t, logs.String(), "DO-NOT-LOG-THIS")
		})
	}
}
