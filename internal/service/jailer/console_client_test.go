package jailer

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/pkg/errs"
)

// Rationale: A receiver may durably stop the relay before its response is lost or rejected. Only an authoritative
// response can resolve that post-start outcome; transport failures must preserve the caller's uncertainty.
func TestConsoleStopClientClassifiesUntrustedResponseByStartState(t *testing.T) {
	t.Parallel()
	request := ConsoleStopRequest{VMID: testVMID}
	valid := encodeConsoleStopClientSuccess(t, ConsoleStopResult{VMID: testVMID, Stopped: true})
	diagnostic := errs.New(errs.CodeProcessError, "receiver exited before authoritative response")
	tests := map[string]consoleStopResponse{
		"pre-start missing":      {diagnostic: diagnostic},
		"pre-start EOF":          {response: valid, responseEOF: true, diagnostic: diagnostic},
		"post-start missing EOF": {response: valid, started: true, diagnostic: diagnostic},
		"post-start malformed": {
			response: []byte("not-a-response"), responseEOF: true, started: true, diagnostic: diagnostic,
		},
		"post-start invalid VM": {
			response: encodeConsoleStopClientSuccess(t,
				ConsoleStopResult{VMID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Stopped: true}),
			responseEOF: true, started: true, diagnostic: diagnostic,
		},
		"post-start missing outcome": {
			response:    encodeConsoleStopClientSuccess(t, map[string]any{"vm_id": testVMID}),
			responseEOF: true, started: true, diagnostic: diagnostic,
		},
	}
	for name, outcome := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := decodeConsoleStopResponse(t.Context(), request, outcome)

			assert.Equal(t, ConsoleStopResult{}, result)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
			assert.Equal(t, outcome.started, domainErr.Details["process_started"])
			assert.Equal(t, outcome.started, domainErr.Details["outcome_unknown"])
			assert.ErrorIs(t, err, diagnostic)
		})
	}
}

// Rationale: Complete EOF-framed operation errors remain authoritative, including their durable effect details,
// even when the process transport separately reports a diagnostic.
func TestConsoleStopClientPreservesAuthoritativeOutcome(t *testing.T) {
	t.Parallel()
	request := ConsoleStopRequest{VMID: testVMID}
	diagnostic := errs.New(errs.CodeProcessError, "non-zero receiver exit")
	remoteErr := errs.New(errs.CodeConsoleRelayFailed, "relay stop wait interrupted",
		errs.WithEntity(testVMID), errs.WithDetails(map[string]any{"relay_stopping": true}))
	var frame bytes.Buffer
	require.NoError(t, privilegedwire.EncodeError(t.Context(), &frame, ConsoleStopAction, remoteErr))

	result, err := decodeConsoleStopResponse(t.Context(), request, consoleStopResponse{
		response: frame.Bytes(), responseEOF: true, started: true, diagnostic: diagnostic,
	})

	assert.Equal(t, ConsoleStopResult{}, result)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, remoteErr.Code, domainErr.Code)
	assert.Equal(t, remoteErr.Class, domainErr.Class)
	assert.Equal(t, remoteErr.Entity, domainErr.Entity)
	assert.Equal(t, remoteErr.Message, domainErr.Message)
	assert.Equal(t, remoteErr.Details, domainErr.Details)
	assert.NotErrorIs(t, err, diagnostic)

	valid := ConsoleStopResult{VMID: testVMID, Stopped: true}
	result, err = decodeConsoleStopResponse(t.Context(), request, consoleStopResponse{
		response: encodeConsoleStopClientSuccess(t, valid), responseEOF: true, started: true, diagnostic: diagnostic,
	})
	require.NoError(t, err)
	assert.Equal(t, valid, result)
}

func encodeConsoleStopClientSuccess(t *testing.T, result any) []byte {
	t.Helper()
	var frame bytes.Buffer
	require.NoError(t, privilegedwire.EncodeSuccess(t.Context(), &frame, ConsoleStopAction, result))
	return frame.Bytes()
}
