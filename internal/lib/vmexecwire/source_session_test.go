package vmexecwire

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

const testSourceToken = "123e4567-e89b-42d3-a456-426614174000"

func TestSourceBootstrapRoundTrip(t *testing.T) {
	request := SourceBootstrap{
		AgentToken:      testSourceToken,
		SourceSessionID: "00112233445566778899aabbccddeeff",
	}
	var wire bytes.Buffer
	require.NoError(t, EncodeSourceBootstrap(t.Context(), &wire, request))
	assert.Equal(t, SourceSessionMagic, wire.String()[:len(SourceSessionMagic)])
	assert.NotEqual(t, FrameMagic, SourceSessionMagic)

	got, err := DecodeSourceBootstrap(t.Context(), &wire)
	require.NoError(t, err)
	assert.Empty(t, cmp.Diff(request, got))
}

func TestSourceReadyRoundTrip(t *testing.T) {
	want := SourceReady{SourceSessionID: "00112233445566778899aabbccddeeff"}
	var wire bytes.Buffer
	require.NoError(t, EncodeSourceReady(t.Context(), &wire, want))

	got, err := DecodeSourceReady(t.Context(), &wire, want.SourceSessionID)
	require.NoError(t, err)
	assert.Empty(t, cmp.Diff(want, got))
}

func TestSourceBootstrapUsesFrozenBoundAndDomainErrors(t *testing.T) {
	assert.Equal(t, 512, MaxSourceBootstrapBytes)

	_, err := DecodeSourceBootstrap(t.Context(), bytes.NewReader(nil))
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = EncodeSourceBootstrap(ctx, io.Discard, SourceBootstrap{
		AgentToken:      testSourceToken,
		SourceSessionID: "00112233445566778899aabbccddeeff",
	})
	require.Error(t, err)
	domainErr = errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.ErrorIs(t, err, context.Canceled)

	err = EncodeSourceBootstrap(t.Context(), failingWriter{}, SourceBootstrap{
		AgentToken:      testSourceToken,
		SourceSessionID: "00112233445566778899aabbccddeeff",
	})
	require.Error(t, err)
	domainErr = errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
}

func TestSourceReadyRejectsMalformedFrames(t *testing.T) {
	tests := map[string]struct {
		body    string
		wantErr string
	}{
		"unknown field": {
			body:    `{"schema_version":1,"source_session_id":"00112233445566778899aabbccddeeff","extra":true}`,
			wantErr: "unknown JSON field",
		},
		"duplicate field": {
			body:    `{"schema_version":1,"source_session_id":"00112233445566778899aabbccddeeff","source_session_id":"00112233445566778899aabbccddeeff"}`,
			wantErr: "duplicate JSON field",
		},
		"case collision": {
			body:    `{"schema_version":1,"source_session_id":"00112233445566778899aabbccddeeff","Source_Session_ID":"00112233445566778899aabbccddeeff"}`,
			wantErr: "case-colliding JSON field",
		},
		"missing field": {
			body:    `{"schema_version":1}`,
			wantErr: `missing required field "source_session_id"`,
		},
		"wrong field type": {
			body:    `{"schema_version":1,"source_session_id":42}`,
			wantErr: "decode VM-exec source-session ready ID",
		},
		"wrong schema type": {
			body:    `{"schema_version":"1","source_session_id":"00112233445566778899aabbccddeeff"}`,
			wantErr: "decode VM-exec source-session schema version",
		},
		"unsupported schema": {
			body:    `{"schema_version":2,"source_session_id":"00112233445566778899aabbccddeeff"}`,
			wantErr: "unsupported source-session schema version",
		},
		"trailing value": {
			body:    `{"schema_version":1,"source_session_id":"00112233445566778899aabbccddeeff"}{}`,
			wantErr: "trailing",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			wire := rawSourceFrame(t, SourceSessionMagic, SourceSessionVersion, []byte(tc.body))
			_, err := DecodeSourceReady(
				t.Context(), bytes.NewReader(wire), "00112233445566778899aabbccddeeff",
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestSourceBootstrapRejectsMalformedFrames(t *testing.T) {
	tests := map[string]struct {
		body    string
		version uint16
		magic   string
		wantErr string
	}{
		"unknown field": {
			body: `{"schema_version":1,"agent_token":"` + testSourceToken +
				`","source_session_id":"00112233445566778899aabbccddeeff","extra":true}`,
			version: SourceSessionVersion,
			magic:   SourceSessionMagic,
			wantErr: "unknown JSON field",
		},
		"duplicate field": {
			body: `{"schema_version":1,"agent_token":"` + testSourceToken +
				`","agent_token":"` + testSourceToken +
				`","source_session_id":"00112233445566778899aabbccddeeff"}`,
			version: SourceSessionVersion,
			magic:   SourceSessionMagic,
			wantErr: "duplicate JSON field",
		},
		"case collision": {
			body: `{"schema_version":1,"agent_token":"` + testSourceToken +
				`","Agent_Token":"` + testSourceToken +
				`","source_session_id":"00112233445566778899aabbccddeeff"}`,
			version: SourceSessionVersion,
			magic:   SourceSessionMagic,
			wantErr: "case-colliding JSON field",
		},
		"unsupported schema": {
			body: `{"schema_version":2,"agent_token":"` + testSourceToken +
				`","source_session_id":"00112233445566778899aabbccddeeff"}`,
			version: SourceSessionVersion,
			magic:   SourceSessionMagic,
			wantErr: "unsupported source-session schema version",
		},
		"unsupported wire version": {
			body: `{"schema_version":1,"agent_token":"` + testSourceToken +
				`","source_session_id":"00112233445566778899aabbccddeeff"}`,
			version: SourceSessionVersion + 1,
			magic:   SourceSessionMagic,
			wantErr: "unsupported source-session frame version",
		},
		"wrong magic": {
			body: `{"schema_version":1,"agent_token":"` + testSourceToken +
				`","source_session_id":"00112233445566778899aabbccddeeff"}`,
			version: SourceSessionVersion,
			magic:   FrameMagic,
			wantErr: "invalid source-session frame magic",
		},
		"invalid token": {
			body:    `{"schema_version":1,"agent_token":"NOT-A-TOKEN","source_session_id":"00112233445566778899aabbccddeeff"}`,
			version: SourceSessionVersion,
			magic:   SourceSessionMagic,
			wantErr: "canonical lowercase UUIDv4",
		},
		"invalid ID": {
			body: `{"schema_version":1,"agent_token":"` + testSourceToken +
				`","source_session_id":"00112233445566778899AABBCCDDEEFF"}`,
			version: SourceSessionVersion,
			magic:   SourceSessionMagic,
			wantErr: "32 lowercase hexadecimal",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			wire := rawSourceFrame(t, tc.magic, tc.version, []byte(tc.body))
			_, err := DecodeSourceBootstrap(context.Background(), bytes.NewReader(wire))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestSourceBootstrapRejectsOversizedFrameBeforeAllocation(t *testing.T) {
	prefix := make([]byte, len(SourceSessionMagic)+2+4)
	copy(prefix, SourceSessionMagic)
	binary.BigEndian.PutUint16(prefix[len(SourceSessionMagic):], SourceSessionVersion)
	binary.BigEndian.PutUint32(prefix[len(SourceSessionMagic)+2:], MaxSourceBootstrapBytes+1)

	_, err := DecodeSourceBootstrap(t.Context(), bytes.NewReader(prefix))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestSourceReadyRequiresMatchingSessionID(t *testing.T) {
	var wire bytes.Buffer
	require.NoError(t, EncodeSourceReady(t.Context(), &wire, SourceReady{
		SourceSessionID: "00112233445566778899aabbccddeeff",
	}))

	_, err := DecodeSourceReady(
		t.Context(),
		&wire,
		"ffeeddccbbaa99887766554433221100",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestRandomRequestAndSourceSessionIDs(t *testing.T) {
	requestID, err := NewRequestID()
	require.NoError(t, err)
	sessionID, err := NewSourceSessionID()
	require.NoError(t, err)
	secondSessionID, err := NewSourceSessionID()
	require.NoError(t, err)

	for _, id := range []string{requestID, sessionID, secondSessionID} {
		assert.Len(t, id, 32)
		assert.NoError(t, validateRequestID(id))
	}
	assert.NotEqual(t, sessionID, secondSessionID)
}

func rawSourceFrame(t *testing.T, magic string, version uint16, body []byte) []byte {
	t.Helper()
	prefix := make([]byte, len(SourceSessionMagic)+2+4)
	copy(prefix, magic)
	binary.BigEndian.PutUint16(prefix[len(SourceSessionMagic):], version)
	binary.BigEndian.PutUint32(prefix[len(SourceSessionMagic)+2:], uint32(len(body)))
	return append(prefix, body...)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
