package vmexecwire_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecwire"
)

const testRequestID = "0123456789abcdef0123456789abcdef"

func TestSourceRequestRoundTrip(t *testing.T) {
	want := vmexecwire.SourceRequest{
		RequestID:   testRequestID,
		Destination: "build-target",
		TargetUser:  "builder",
		Command:     "printf 'ready'",
	}

	var encoded bytes.Buffer
	require.NoError(t, vmexecwire.EncodeSourceRequest(t.Context(), &encoded, want))
	got, err := vmexecwire.DecodeSourceRequest(t.Context(), bytes.NewReader(encoded.Bytes()))
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("source request mismatch (-want +got):\n%s", diff)
	}
}

func TestDestinationRequestRoundTrip(t *testing.T) {
	want := vmexecwire.DestinationRequest{
		RequestID:  testRequestID,
		AgentToken: "01234567-89ab-4def-8123-456789abcdef",
		TargetUser: "builder",
		Command:    "printf 'ready'",
	}

	var encoded bytes.Buffer
	require.NoError(t, vmexecwire.EncodeDestinationRequest(t.Context(), &encoded, want))
	got, err := vmexecwire.DecodeDestinationRequest(t.Context(), bytes.NewReader(encoded.Bytes()))
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("destination request mismatch (-want +got):\n%s", diff)
	}
}

func TestDestinationRequestValidationRejectsUnsafeValues(t *testing.T) {
	valid := func() vmexecwire.DestinationRequest {
		return vmexecwire.DestinationRequest{
			RequestID:  testRequestID,
			AgentToken: "01234567-89ab-4def-8123-456789abcdef",
			TargetUser: "builder",
			Command:    "printf ready",
		}
	}
	tests := map[string]struct {
		mutate  func(*vmexecwire.DestinationRequest)
		wantErr string
	}{
		"empty agent token": {
			mutate:  func(request *vmexecwire.DestinationRequest) { request.AgentToken = "" },
			wantErr: "agent token",
		},
		"uppercase agent token": {
			mutate: func(request *vmexecwire.DestinationRequest) {
				request.AgentToken = strings.ToUpper(request.AgentToken)
			},
			wantErr: "agent token",
		},
		"wrong UUID version": {
			mutate: func(request *vmexecwire.DestinationRequest) {
				request.AgentToken = "01234567-89ab-3def-8123-456789abcdef"
			},
			wantErr: "agent token",
		},
		"root target": {
			mutate:  func(request *vmexecwire.DestinationRequest) { request.TargetUser = "root" },
			wantErr: "non-root",
		},
		"invalid target": {
			mutate:  func(request *vmexecwire.DestinationRequest) { request.TargetUser = "user;id" },
			wantErr: "target user",
		},
		"oversized command": {
			mutate: func(request *vmexecwire.DestinationRequest) {
				request.Command = strings.Repeat("a", vmexecwire.MaxCommandBytes+1)
			},
			wantErr: "command",
		},
		"unsafe command": {
			mutate:  func(request *vmexecwire.DestinationRequest) { request.Command = "printf \x00" },
			wantErr: "command",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			request := valid()
			test.mutate(&request)
			err := vmexecwire.EncodeDestinationRequest(t.Context(), &bytes.Buffer{}, request)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestDecodeDestinationRequestRejectsMalformedFrames(t *testing.T) {
	validBody := `{"schema_version":1,"request_id":"` + testRequestID +
		`","agent_token":"01234567-89ab-4def-8123-456789abcdef","target_user":"builder","command":"id"}`
	tests := map[string]struct {
		body    string
		wantErr string
	}{
		"source-only field": {
			body: `{"schema_version":1,"request_id":"` + testRequestID +
				`","destination":"target","target_user":"builder","command":"id",` +
				`"agent_token":"01234567-89ab-4def-8123-456789abcdef"}`,
			wantErr: "unknown",
		},
		"missing token": {
			body: `{"schema_version":1,"request_id":"` + testRequestID +
				`","target_user":"builder","command":"id"}`,
			wantErr: "missing",
		},
		"duplicate token": {
			body: validBody[:len(validBody)-1] +
				`,"agent_token":"01234567-89ab-4def-8123-456789abcdef"}`,
			wantErr: "duplicate",
		},
		"case-colliding token": {
			body:    validBody[:len(validBody)-1] + `,"Agent_Token":"value"}`,
			wantErr: "case",
		},
		"wrong schema": {
			body:    strings.Replace(validBody, `"schema_version":1`, `"schema_version":2`, 1),
			wantErr: "schema",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := vmexecwire.DecodeDestinationRequest(
				t.Context(),
				bytes.NewReader(requestFrameForTest(test.body)),
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestRequestValidationRejectsUnboundedOrUnsafeValues(t *testing.T) {
	// Rationale: Guest-controlled request fields cross two trust boundaries. Each boundary must reject malformed
	// identity, privilege, text, and allocation inputs before the relay or destination agent acts on them.
	valid := func() vmexecwire.SourceRequest {
		return vmexecwire.SourceRequest{
			RequestID:   testRequestID,
			Destination: "build-target",
			TargetUser:  "builder",
			Command:     "printf ready",
		}
	}
	tests := map[string]struct {
		mutate  func(*vmexecwire.SourceRequest)
		wantErr string
	}{
		"empty request ID": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.RequestID = "" },
			wantErr: "request ID",
		},
		"uppercase request ID": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.RequestID = strings.ToUpper(testRequestID) },
			wantErr: "request ID",
		},
		"non-hex request ID": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.RequestID = strings.Repeat("g", 32) },
			wantErr: "request ID",
		},
		"root target": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.TargetUser = "root" },
			wantErr: "non-root",
		},
		"invalid target user": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.TargetUser = "user;id" },
			wantErr: "target user",
		},
		"empty destination": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.Destination = "" },
			wantErr: "destination",
		},
		"destination control character": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.Destination = "target\nother" },
			wantErr: "destination",
		},
		"empty command": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.Command = "" },
			wantErr: "command",
		},
		"oversized command": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.Command = strings.Repeat("a", 32*1024+1) },
			wantErr: "command",
		},
		"command control character": {
			mutate:  func(request *vmexecwire.SourceRequest) { request.Command = "printf \x00" },
			wantErr: "command",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			request := valid()
			test.mutate(&request)

			var encoded bytes.Buffer
			err := vmexecwire.EncodeSourceRequest(t.Context(), &encoded, request)

			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestDecodeSourceRequestRejectsMalformedFrames(t *testing.T) {
	// Rationale: A permissive JSON decoder can silently accept alternate field spellings or attacker-selected
	// extensions. The source boundary admits one exact schema and one complete bounded frame.
	validBody := `{"schema_version":1,"request_id":"` + testRequestID +
		`","destination":"build-target","target_user":"builder","command":"id"}`
	tests := map[string]struct {
		frame   []byte
		wantErr string
	}{
		"unknown field": {
			frame:   requestFrameForTest(validBody[:len(validBody)-1] + `,"extra":true}`),
			wantErr: "unknown",
		},
		"missing field": {
			frame: requestFrameForTest(`{"schema_version":1,"request_id":"` + testRequestID +
				`","destination":"build-target","target_user":"builder"}`),
			wantErr: "missing",
		},
		"duplicate field": {
			frame: requestFrameForTest(`{"schema_version":1,"request_id":"` + testRequestID +
				`","request_id":"` + testRequestID +
				`","destination":"build-target","target_user":"builder","command":"id"}`),
			wantErr: "duplicate",
		},
		"case-colliding field": {
			frame:   requestFrameForTest(validBody[:len(validBody)-1] + `,"Command":"id"}`),
			wantErr: "case",
		},
		"wrong schema": {
			frame:   requestFrameForTest(strings.Replace(validBody, `"schema_version":1`, `"schema_version":2`, 1)),
			wantErr: "schema",
		},
		"invalid UTF-8": {
			frame:   requestFrameForTest(string([]byte{'{', 0xff, '}'})),
			wantErr: "UTF-8",
		},
		"trailing JSON": {
			frame:   requestFrameForTest(validBody + `{}`),
			wantErr: "trailing",
		},
		"oversized declared length": {
			frame:   requestPrefixForTest(64*1024 + 1),
			wantErr: "65536",
		},
		"agent token injection": {
			frame: requestFrameForTest(validBody[:len(validBody)-1] +
				`,"agent_token":"01234567-89ab-4def-8123-456789abcdef"}`),
			wantErr: "unknown",
		},
		"invalid magic": {
			frame:   mutateRequestFrameForTest(validBody, func(frame []byte) { frame[0] = 'X' }),
			wantErr: "magic",
		},
		"unsupported wire version": {
			frame: mutateRequestFrameForTest(validBody, func(frame []byte) {
				binary.BigEndian.PutUint16(frame[8:10], 2)
			}),
			wantErr: "version",
		},
		"truncated body": {
			frame: mutateRequestFrameForTest(validBody, func(frame []byte) {
				binary.BigEndian.PutUint32(frame[10:14], uint32(len(validBody)+1))
			}),
			wantErr: "body",
		},
		"empty body": {
			frame:   requestPrefixForTest(0),
			wantErr: "empty",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := vmexecwire.DecodeSourceRequest(t.Context(), bytes.NewReader(test.frame))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestEncodeRequestHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := vmexecwire.SourceRequest{
		RequestID: testRequestID, Destination: "target", TargetUser: "builder", Command: "id",
	}

	err := vmexecwire.EncodeSourceRequest(ctx, &bytes.Buffer{}, request)

	assert.ErrorIs(t, err, context.Canceled)
}

func requestFrameForTest(body string) []byte {
	prefix := requestPrefixForTest(len(body))
	return append(prefix, []byte(body)...)
}

func requestPrefixForTest(length int) []byte {
	prefix := make([]byte, 14)
	copy(prefix, "MVMEXEC1")
	binary.BigEndian.PutUint16(prefix[8:10], 1)
	binary.BigEndian.PutUint32(prefix[10:14], uint32(length))
	return prefix
}

func mutateRequestFrameForTest(body string, mutate func([]byte)) []byte {
	frame := requestFrameForTest(body)
	mutate(frame)
	return frame
}
