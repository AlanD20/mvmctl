package vmexecsessionwire_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

const (
	testSourceVMID      = "0123456789abcdef0123456789abcdef"
	testDestinationVMID = "fedcba9876543210fedcba9876543210"
)

func TestOpenSourceRoundTrip(t *testing.T) {
	want := vmexecsessionwire.OpenSource{
		SourceVMID:      testSourceVMID,
		DestinationVMID: testDestinationVMID,
	}
	var encoded bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeOpenSource(t.Context(), &encoded, want))

	got, err := vmexecsessionwire.DecodeOpenSource(t.Context(), &encoded)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("open source mismatch (-want +got):\n%s", diff)
	}
}

func TestFixedPhaseRoundTrips(t *testing.T) {
	tests := map[string]struct {
		encode func(context.Context, io.Writer) error
		decode func(context.Context, io.Reader) error
	}{
		"source_retained": {
			encode: vmexecsessionwire.EncodeSourceRetained,
			decode: vmexecsessionwire.DecodeSourceRetained,
		},
		"bind_destination": {
			encode: vmexecsessionwire.EncodeBindDestination,
			decode: vmexecsessionwire.DecodeBindDestination,
		},
		"ready": {
			encode: vmexecsessionwire.EncodeReady,
			decode: vmexecsessionwire.DecodeReady,
		},
		"release": {
			encode: vmexecsessionwire.EncodeRelease,
			decode: vmexecsessionwire.DecodeRelease,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var encoded bytes.Buffer
			require.NoError(t, test.encode(t.Context(), &encoded))
			assert.NoError(t, test.decode(t.Context(), &encoded))
		})
	}
}

func TestTerminalRoundTrips(t *testing.T) {
	t.Run("released", func(t *testing.T) {
		var encoded bytes.Buffer
		require.NoError(t, vmexecsessionwire.EncodeReleased(t.Context(), &encoded))

		outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), &encoded)
		require.NoError(t, err)
		assert.True(t, outcome.Released())
		assert.Nil(t, outcome.OperationError())
	})

	t.Run("failure", func(t *testing.T) {
		operationErr := errs.New(
			errs.CodeProcessError,
			"retained VM-exec session failed",
			errs.WithClass(errs.ClassRetryable),
			errs.WithEntity(testSourceVMID),
			errs.WithDetails(map[string]any{
				"process_started": false,
				"outcome_unknown": true,
			}),
		)
		operationErr.Op = "vmexec.retain"
		var encoded bytes.Buffer
		require.NoError(t, vmexecsessionwire.EncodeFailure(t.Context(), &encoded, operationErr))

		outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), &encoded)
		require.NoError(t, err)
		assert.False(t, outcome.Released())
		require.NotNil(t, outcome.OperationError())
		got := outcome.OperationError()
		assert.Equal(t, errs.CodeProcessError, got.Code)
		assert.Equal(t, errs.ClassRetryable, got.Class)
		assert.Equal(t, "retained VM-exec session failed", got.Message)
		assert.Equal(t, "vmexec.retain", got.Op)
		assert.Equal(t, testSourceVMID, got.Entity)
		assert.Equal(t, false, got.Details["process_started"])
		assert.Equal(t, true, got.Details["outcome_unknown"])
	})
}

func TestDecodeOpenSourceRejectsStrictGrammar(t *testing.T) {
	valid := `{"schema_version":1,"phase":"open_source","source_vm_id":"` + testSourceVMID +
		`","destination_vm_id":"` + testDestinationVMID + `"}`
	tests := map[string]string{
		"unknown_field": strings.TrimSuffix(valid, "}") + `,"command":"id"}`,
		"duplicate_field": `{"schema_version":1,"phase":"open_source","source_vm_id":"` + testSourceVMID +
			`","source_vm_id":"` + testSourceVMID + `","destination_vm_id":"` + testDestinationVMID + `"}`,
		"case_folded_field": `{"schema_version":1,"phase":"open_source","source_vm_id":"` + testSourceVMID +
			`","Source_VM_ID":"` + testSourceVMID + `","destination_vm_id":"` + testDestinationVMID + `"}`,
		"trailing_json": valid + `{}`,
		"null":          `null`,
		"malformed":     `{"schema_version":1`,
		"short":         valid[:len(valid)-1],
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := vmexecsessionwire.DecodeOpenSource(t.Context(), bytes.NewReader(testFrame(body)))
			assert.Error(t, err)
		})
	}
}

func TestOpenSourceRejectsInvalidVMIdentities(t *testing.T) {
	tests := map[string]vmexecsessionwire.OpenSource{
		"uppercase source": {
			SourceVMID: strings.ToUpper(testSourceVMID), DestinationVMID: testDestinationVMID,
		},
		"short source": {
			SourceVMID: testSourceVMID[:31], DestinationVMID: testDestinationVMID,
		},
		"non-hex source": {
			SourceVMID: strings.Repeat("g", 32), DestinationVMID: testDestinationVMID,
		},
		"uppercase destination": {
			SourceVMID: testSourceVMID, DestinationVMID: strings.ToUpper(testDestinationVMID),
		},
		"short destination": {
			SourceVMID: testSourceVMID, DestinationVMID: testDestinationVMID[:31],
		},
		"non-hex destination": {
			SourceVMID: testSourceVMID, DestinationVMID: strings.Repeat("g", 32),
		},
		"same VM": {SourceVMID: testSourceVMID, DestinationVMID: testSourceVMID},
	}

	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, vmexecsessionwire.EncodeOpenSource(t.Context(), io.Discard, request))
		})
	}
}

func TestDecodeOpenSourceRejectsInvalidDestinationIdentity(t *testing.T) {
	// Rationale: The encoder is not the trust boundary. Root must independently reject a malformed destination ID
	// received from a forged caller even when the source identity is canonical.
	body := `{"schema_version":1,"phase":"open_source","source_vm_id":"` + testSourceVMID +
		`","destination_vm_id":"` + strings.ToUpper(testDestinationVMID) + `"}`

	_, err := vmexecsessionwire.DecodeOpenSource(t.Context(), bytes.NewReader(testFrame(body)))

	assert.Error(t, err)
}

func TestPhaseDecodersRejectWrongOrExtendedSchemas(t *testing.T) {
	tests := map[string]struct {
		body   string
		decode func(context.Context, io.Reader) error
	}{
		"wrong_phase": {
			body:   `{"schema_version":1,"phase":"ready"}`,
			decode: vmexecsessionwire.DecodeSourceRetained,
		},
		"extra_field": {
			body:   `{"schema_version":1,"phase":"ready","token":"secret"}`,
			decode: vmexecsessionwire.DecodeReady,
		},
		"wrong_version": {
			body:   `{"schema_version":2,"phase":"release"}`,
			decode: vmexecsessionwire.DecodeRelease,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, test.decode(t.Context(), bytes.NewReader(testFrame(test.body))))
		})
	}
}

func TestFrameRejectsInvalidEnvelopeAndBounds(t *testing.T) {
	tests := map[string][]byte{
		"bad_magic":    append([]byte("BADMAGIC\x00\x01\x00\x00\x00\x02{}"), nil...),
		"bad_version":  append([]byte("MVMXSES1\x00\x02\x00\x00\x00\x02{}"), nil...),
		"empty":        append([]byte("MVMXSES1\x00\x01\x00\x00\x00\x00"), nil...),
		"oversized":    testPrefix(vmexecsessionwire.MaxFrameBytes + 1),
		"short_prefix": []byte("MVMX"),
		"short_body":   append(testPrefix(8), []byte("{}")...),
	}

	for name, frame := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := vmexecsessionwire.DecodeOpenSource(t.Context(), bytes.NewReader(frame))
			assert.Error(t, err)
		})
	}
}

func TestCodecsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := vmexecsessionwire.EncodeOpenSource(ctx, io.Discard, vmexecsessionwire.OpenSource{
		SourceVMID:      testSourceVMID,
		DestinationVMID: testDestinationVMID,
	})
	assert.ErrorIs(t, err, context.Canceled)

	_, err = vmexecsessionwire.DecodeOpenSource(ctx, bytes.NewReader(nil))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFailureProjectionDropsSensitiveAndUnsupportedDetails(t *testing.T) {
	operationErr := errs.New(
		errs.CodeInternal,
		"retained session failed",
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{
			"stderr":            "secret output",
			"command":           "cat /root/token",
			"outcome_unknown":   true,
			"unsupported_value": map[string]string{"path": "/root"},
		}),
	)
	var encoded bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeFailure(t.Context(), &encoded, operationErr))
	assert.NotContains(t, encoded.String(), "secret output")
	assert.NotContains(t, encoded.String(), "cat /root/token")
	assert.NotContains(t, encoded.String(), "/root")

	outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), &encoded)
	require.NoError(t, err)
	require.NotNil(t, outcome.OperationError())
	assert.Equal(t, true, outcome.OperationError().Details["outcome_unknown"])
	assert.Equal(t, true, outcome.OperationError().Details["session_details_omitted"])
}

func TestFailureProjectionDropsWrappedCause(t *testing.T) {
	// Rationale: A root-side cause can contain host paths, command output, or credentials. Only the curated
	// DomainError projection may cross to the unprivileged caller.
	operationErr := errs.WrapMsg(
		errs.CodeInternal,
		"retained session failed",
		io.ErrUnexpectedEOF,
	)
	var encoded bytes.Buffer

	require.NoError(t, vmexecsessionwire.EncodeFailure(t.Context(), &encoded, operationErr))

	assert.NotContains(t, encoded.String(), io.ErrUnexpectedEOF.Error())
	outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), &encoded)
	require.NoError(t, err)
	require.NotNil(t, outcome.OperationError())
	assert.Nil(t, outcome.OperationError().Err)
}

func TestDecodeOutcomeRejectsUntrustedFailureSchemas(t *testing.T) {
	validError := `{"code":"internal","class":"internal","message":"session failed",` +
		`"operation":"vmexec.retain","entity":"` + testSourceVMID + `","details":{}}`
	tests := map[string]string{
		"unknown outer field": `{"schema_version":1,"phase":"failure","error":` + validError +
			`,"command":"id"}`,
		"duplicate nested field": `{"schema_version":1,"phase":"failure","error":` +
			`{"code":"internal","code":"internal","class":"internal","message":"session failed",` +
			`"operation":"","entity":"","details":{}}}`,
		"case-colliding nested field": `{"schema_version":1,"phase":"failure","error":` +
			`{"code":"internal","Code":"internal","class":"internal","message":"session failed",` +
			`"operation":"","entity":"","details":{}}}`,
		"null error":           `{"schema_version":1,"phase":"failure","error":null}`,
		"wrong schema":         `{"schema_version":2,"phase":"failure","error":` + validError + `}`,
		"wrong terminal phase": `{"schema_version":1,"phase":"ready"}`,
		"unsafe detail key": `{"schema_version":1,"phase":"failure","error":` +
			`{"code":"internal","class":"internal","message":"session failed","operation":"",` +
			`"entity":"","details":{"stderr":"secret"}}}`,
		"unsafe detail type": `{"schema_version":1,"phase":"failure","error":` +
			`{"code":"internal","class":"internal","message":"session failed","operation":"",` +
			`"entity":"","details":{"outcome_unknown":"true"}}}`,
		"trailing JSON": `{"schema_version":1,"phase":"failure","error":` + validError + `}{}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := vmexecsessionwire.DecodeOutcome(t.Context(), bytes.NewReader(testFrame(body)))
			assert.Error(t, err)
		})
	}
}

func TestActionIsFixed(t *testing.T) {
	assert.Equal(t, "jailer-vm-exec-session", vmexecsessionwire.Action)
}

func testFrame(body string) []byte {
	prefix := testPrefix(uint32(len(body)))
	return append(prefix, body...)
}

func testPrefix(length uint32) []byte {
	prefix := make([]byte, 14)
	copy(prefix, "MVMXSES1")
	binary.BigEndian.PutUint16(prefix[8:10], 1)
	binary.BigEndian.PutUint32(prefix[10:14], length)
	return prefix
}
