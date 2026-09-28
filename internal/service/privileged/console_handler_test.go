package privileged

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
)

func TestConsoleStopHandlerRequiresEOFBeforeEffect(t *testing.T) {
	const vmID = "22222222222222222222222222222222"
	for _, trailing := range []bool{false, true} {
		name := "complete request"
		if trailing {
			name = "trailing input"
		}
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			var frame bytes.Buffer
			require.NoError(t, privilegedwire.EncodeRequest(t.Context(), &frame, jailer.ConsoleStopAction,
				jailer.ConsoleStopRequest{VMID: vmID}, 0))
			if trailing {
				frame.WriteByte(1)
			}
			endpoint.input = *bytes.NewReader(frame.Bytes())
			stopped := false
			caller := callerIdentity{uid: 1000, gid: 1001}
			stop := func(_ context.Context, got jailer.ConsoleCaller, request jailer.ConsoleStopRequest) (
				jailer.ConsoleStopResult, error,
			) {
				assert.Equal(t, jailer.ConsoleCaller{UID: 1000, GID: 1001}, got)
				assert.Equal(t, vmID, request.VMID)
				stopped = true
				return jailer.ConsoleStopResult{VMID: vmID, Stopped: true}, nil
			}
			require.NoError(t, handleConsoleStop(t.Context(), endpoint, caller, stop,
				trustedReleaseControlDeps(t, endpoint)))
			response, err := privilegedwire.DecodeResponse[jailer.ConsoleStopResult](t.Context(),
				bytes.NewReader(endpoint.output.Bytes()), jailer.ConsoleStopAction)
			require.NoError(t, err)
			if trailing {
				require.Error(t, response.OperationError())
				assert.False(t, stopped)
				return
			}
			require.NoError(t, response.OperationError())
			assert.Equal(t, jailer.ConsoleStopResult{VMID: vmID, Stopped: true}, response.Result())
			assert.True(t, stopped)
		})
	}
}
