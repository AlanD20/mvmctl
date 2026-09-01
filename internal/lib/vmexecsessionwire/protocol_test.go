package vmexecsessionwire_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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
	testMaxFrameBytes   = 64 * 1024
)

func TestOpenSourceCodecMatchesContract(t *testing.T) {
	want := vmexecsessionwire.OpenSource{
		SourceVMID:      testSourceVMID,
		DestinationVMID: testDestinationVMID,
	}
	literal := `{"schema_version":1,"phase":"open_source","source_vm_id":"` + testSourceVMID +
		`","destination_vm_id":"` + testDestinationVMID + `"}`

	t.Run("encoder", func(t *testing.T) {
		var encoded bytes.Buffer
		require.NoError(t, vmexecsessionwire.EncodeOpenSource(t.Context(), &encoded, want))
		assert.Equal(t, literal, testFrameBody(t, encoded.Bytes()))
	})

	t.Run("decoder", func(t *testing.T) {
		got, err := vmexecsessionwire.DecodeOpenSource(t.Context(), bytes.NewReader(testFrame(literal)))
		require.NoError(t, err)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("open source mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPhaseEncodersMatchContractLiterals(t *testing.T) {
	tests := map[string]struct {
		encode func(context.Context, io.Writer) error
		body   string
	}{
		"source_retained": {
			encode: vmexecsessionwire.EncodeSourceRetained,
			body:   `{"schema_version":1,"phase":"source_retained"}`,
		},
		"bind_destination": {
			encode: vmexecsessionwire.EncodeBindDestination,
			body:   `{"schema_version":1,"phase":"bind_destination"}`,
		},
		"ready": {
			encode: vmexecsessionwire.EncodeReady,
			body:   `{"schema_version":1,"phase":"ready"}`,
		},
		"release": {
			encode: vmexecsessionwire.EncodeRelease,
			body:   `{"schema_version":1,"phase":"release"}`,
		},
		"released": {
			encode: vmexecsessionwire.EncodeReleased,
			body:   `{"schema_version":1,"phase":"released"}`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var encoded bytes.Buffer
			require.NoError(t, test.encode(t.Context(), &encoded))
			assert.Equal(t, test.body, testFrameBody(t, encoded.Bytes()))
		})
	}
}

func TestPhaseDecodersAcceptContractLiterals(t *testing.T) {
	t.Run("source retained", func(t *testing.T) {
		result, err := vmexecsessionwire.DecodeSourceRetained(
			t.Context(),
			bytes.NewReader(testFrame(`{"schema_version":1,"phase":"source_retained"}`)),
		)
		require.NoError(t, err)
		assert.True(t, result.Retained())
		assert.Nil(t, result.OperationError())
	})

	t.Run("bind destination", func(t *testing.T) {
		err := vmexecsessionwire.DecodeBindDestination(
			t.Context(),
			bytes.NewReader(testFrame(`{"schema_version":1,"phase":"bind_destination"}`)),
		)
		assert.NoError(t, err)
	})

	t.Run("ready", func(t *testing.T) {
		result, err := vmexecsessionwire.DecodeReady(
			t.Context(),
			bytes.NewReader(testFrame(`{"schema_version":1,"phase":"ready"}`)),
		)
		require.NoError(t, err)
		assert.True(t, result.Ready())
		assert.Nil(t, result.OperationError())
	})

	t.Run("release", func(t *testing.T) {
		err := vmexecsessionwire.DecodeRelease(
			t.Context(),
			bytes.NewReader(testFrame(`{"schema_version":1,"phase":"release"}`)),
		)
		assert.NoError(t, err)
	})
}

func TestSetupRepliesAcceptSuccessOrTerminalFailure(t *testing.T) {
	t.Run("source success keeps stream open", func(t *testing.T) {
		var stream bytes.Buffer
		require.NoError(t, vmexecsessionwire.EncodeSourceRetained(t.Context(), &stream))
		require.NoError(t, vmexecsessionwire.EncodeBindDestination(t.Context(), &stream))

		result, err := vmexecsessionwire.DecodeSourceRetained(t.Context(), &stream)
		require.NoError(t, err)
		assert.True(t, result.Retained())
		assert.Nil(t, result.OperationError())
		assert.NoError(t, vmexecsessionwire.DecodeBindDestination(t.Context(), &stream))
	})

	t.Run("ready success", func(t *testing.T) {
		var encoded bytes.Buffer
		require.NoError(t, vmexecsessionwire.EncodeReady(t.Context(), &encoded))

		result, err := vmexecsessionwire.DecodeReady(t.Context(), &encoded)
		require.NoError(t, err)
		assert.True(t, result.Ready())
		assert.Nil(t, result.OperationError())
	})

	for _, phase := range []string{"source retained", "ready"} {
		t.Run(phase+" failure", func(t *testing.T) {
			operationErr := errs.New(errs.CodeProcessError, "private root cause")
			var encoded bytes.Buffer
			require.NoError(t, vmexecsessionwire.EncodeFailure(t.Context(), &encoded, operationErr))

			if phase == "source retained" {
				result, err := vmexecsessionwire.DecodeSourceRetained(t.Context(), &encoded)
				require.NoError(t, err)
				assert.False(t, result.Retained())
				require.NotNil(t, result.OperationError())
				assert.Equal(t, errs.CodeProcessError, result.OperationError().Code)
				return
			}
			result, err := vmexecsessionwire.DecodeReady(t.Context(), &encoded)
			require.NoError(t, err)
			assert.False(t, result.Ready())
			require.NotNil(t, result.OperationError())
			assert.Equal(t, errs.CodeProcessError, result.OperationError().Code)
		})

		t.Run(phase+" failure requires EOF", func(t *testing.T) {
			var encoded bytes.Buffer
			require.NoError(t, vmexecsessionwire.EncodeFailure(
				t.Context(),
				&encoded,
				errs.New(errs.CodeProcessError, "private root cause"),
			))
			encoded.WriteByte('x')

			if phase == "source retained" {
				_, err := vmexecsessionwire.DecodeSourceRetained(t.Context(), &encoded)
				assertValidationError(t, err)
				return
			}
			_, err := vmexecsessionwire.DecodeReady(t.Context(), &encoded)
			assertValidationError(t, err)
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
		assert.Equal(t, "retained VM-exec session process failed", got.Message)
		assert.Equal(t, "vmexec.retain", got.Op)
		assert.Equal(t, testSourceVMID, got.Entity)
		assert.Equal(t, false, got.Details["process_started"])
		assert.Equal(t, true, got.Details["outcome_unknown"])
	})
}

func TestTerminalOutcomeRequiresEOF(t *testing.T) {
	for _, phase := range []string{"released", "failure"} {
		t.Run(phase, func(t *testing.T) {
			var encoded bytes.Buffer
			if phase == "released" {
				require.NoError(t, vmexecsessionwire.EncodeReleased(t.Context(), &encoded))
			} else {
				require.NoError(t, vmexecsessionwire.EncodeFailure(
					t.Context(),
					&encoded,
					errs.New(errs.CodeInternal, "private failure"),
				))
			}
			encoded.WriteByte('x')

			_, err := vmexecsessionwire.DecodeOutcome(t.Context(), &encoded)

			assertValidationError(t, err)
		})
	}
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

func TestDecodeOpenSourceRejectsForgedVMIdentities(t *testing.T) {
	// Rationale: The encoder is not the trust boundary. Root must independently reject either malformed endpoint and
	// a same-VM pair received from a forged caller.
	tests := map[string]struct {
		source      string
		destination string
	}{
		"short source": {
			source: testSourceVMID[:31], destination: testDestinationVMID,
		},
		"non-hex source": {
			source: strings.Repeat("g", 32), destination: testDestinationVMID,
		},
		"uppercase source": {
			source: strings.ToUpper(testSourceVMID), destination: testDestinationVMID,
		},
		"short destination": {
			source: testSourceVMID, destination: testDestinationVMID[:31],
		},
		"non-hex destination": {
			source: testSourceVMID, destination: strings.Repeat("g", 32),
		},
		"uppercase destination": {
			source: testSourceVMID, destination: strings.ToUpper(testDestinationVMID),
		},
		"same VM": {
			source: testSourceVMID, destination: testSourceVMID,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			body := `{"schema_version":1,"phase":"open_source","source_vm_id":"` + test.source +
				`","destination_vm_id":"` + test.destination + `"}`
			_, err := vmexecsessionwire.DecodeOpenSource(t.Context(), bytes.NewReader(testFrame(body)))
			assertValidationError(t, err)
		})
	}
}

func TestPhaseDecodersRejectWrongOrExtendedSchemas(t *testing.T) {
	tests := map[string]struct {
		body   string
		decode func(context.Context, io.Reader) error
	}{
		"wrong_phase": {
			body: `{"schema_version":1,"phase":"ready"}`,
			decode: func(ctx context.Context, reader io.Reader) error {
				_, err := vmexecsessionwire.DecodeSourceRetained(ctx, reader)
				return err
			},
		},
		"extra_field": {
			body: `{"schema_version":1,"phase":"ready","token":"secret"}`,
			decode: func(ctx context.Context, reader io.Reader) error {
				_, err := vmexecsessionwire.DecodeReady(ctx, reader)
				return err
			},
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
		"oversized":    testPrefix(testMaxFrameBytes + 1),
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

func TestDecoderAcceptsExactMaximumFrame(t *testing.T) {
	body := []byte(`{"schema_version":1,"phase":"ready"}`)
	body = append(body, bytes.Repeat([]byte(" "), testMaxFrameBytes-len(body))...)

	result, err := vmexecsessionwire.DecodeReady(t.Context(), bytes.NewReader(testFrame(string(body))))

	require.NoError(t, err)
	assert.True(t, result.Ready())
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
	assert.NotContains(t, encoded.String(), "retained session failed")
	assert.NotContains(t, encoded.String(), "secret output")
	assert.NotContains(t, encoded.String(), "cat /root/token")
	assert.NotContains(t, encoded.String(), "/root")

	outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), &encoded)
	require.NoError(t, err)
	require.NotNil(t, outcome.OperationError())
	assert.Equal(t, true, outcome.OperationError().Details["outcome_unknown"])
	assert.Equal(t, true, outcome.OperationError().Details["session_details_omitted"])
}

func TestFailureProjectionUsesFixedPublicMessages(t *testing.T) {
	tests := map[string]struct {
		operationErr error
		wantCode     errs.Code
		wantMessage  string
	}{
		"validation": {
			operationErr: errs.New(errs.CodeValidationFailed, "secret validation /root/token"),
			wantCode:     errs.CodeValidationFailed,
			wantMessage:  "retained VM-exec session request was invalid",
		},
		"process": {
			operationErr: errs.New(errs.CodeProcessError, "secret process output"),
			wantCode:     errs.CodeProcessError,
			wantMessage:  "retained VM-exec session process failed",
		},
		"atomic": {
			operationErr: errs.New(errs.CodeVMAtomicFailed, "secret atomic state"),
			wantCode:     errs.CodeVMAtomicFailed,
			wantMessage:  "retained VM-exec session transition failed",
		},
		"capacity": {
			operationErr: errs.New(errs.CodeVMResourceExhausted, "secret capacity state"),
			wantCode:     errs.CodeVMResourceExhausted,
			wantMessage:  "retained VM-exec session capacity is exhausted",
		},
		"unauthorized": {
			operationErr: errs.New(errs.CodeUnauthorized, "secret peer identity"),
			wantCode:     errs.CodeUnauthorized,
			wantMessage:  "retained VM-exec session is not authorized",
		},
		"internal": {
			operationErr: errs.New(errs.CodeInternal, "secret internal /root/path"),
			wantCode:     errs.CodeInternal,
			wantMessage:  "retained VM-exec session failed",
		},
		"unknown domain error": {
			operationErr: errs.New(errs.Code("private.secret"), "unknown secret"),
			wantCode:     errs.CodeInternal,
			wantMessage:  "retained VM-exec session failed",
		},
		"plain error": {
			operationErr: errors.New("plain secret /root/path"),
			wantCode:     errs.CodeInternal,
			wantMessage:  "retained VM-exec session failed",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var encoded bytes.Buffer
			require.NoError(t, vmexecsessionwire.EncodeFailure(t.Context(), &encoded, test.operationErr))
			assert.NotContains(t, encoded.String(), "secret")
			assert.NotContains(t, encoded.String(), "/root")

			outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), &encoded)
			require.NoError(t, err)
			require.NotNil(t, outcome.OperationError())
			assert.Equal(t, test.wantCode, outcome.OperationError().Code)
			assert.Equal(t, test.wantMessage, outcome.OperationError().Message)
		})
	}
}

func TestFailureProjectionOwnsOmissionMarkerAndStaysDecodable(t *testing.T) {
	details := map[string]any{
		"session_details_omitted": false,
		"safe.token.value":        int64(99),
	}
	for index := range 20 {
		details[fmt.Sprintf("safe_%02d", index)] = int64(index)
	}
	operationErr := errs.New(errs.CodeInternal, "private", errs.WithDetails(details))
	var encoded bytes.Buffer

	require.NoError(t, vmexecsessionwire.EncodeFailure(t.Context(), &encoded, operationErr))
	outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), &encoded)
	require.NoError(t, err)
	require.NotNil(t, outcome.OperationError())
	want := map[string]any{"session_details_omitted": true}
	for index := range 15 {
		want[fmt.Sprintf("safe_%02d", index)] = int64(index)
	}
	if diff := cmp.Diff(want, outcome.OperationError().Details); diff != "" {
		t.Errorf("decoded retained details mismatch (-want +got):\n%s", diff)
	}
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

func TestDecodeOutcomeRejectsInvalidFailureSemantics(t *testing.T) {
	detailFields := make([]string, 17)
	for index := range detailFields {
		detailFields[index] = fmt.Sprintf(`"safe_%02d":true`, index)
	}
	tests := map[string]string{
		"unknown code": testFailureBody(
			"private.secret", "internal", "retained VM-exec session failed", "", "", `{}`,
		),
		"invalid class": testFailureBody(
			"internal", "private", "retained VM-exec session failed", "", "", `{}`,
		),
		"arbitrary message": testFailureBody(
			"internal", "internal", "private root output", "", "", `{}`,
		),
		"unsafe operation": testFailureBody(
			"internal", "internal", "retained VM-exec session failed", "VMExec Retain", "", `{}`,
		),
		"non-hex entity": testFailureBody(
			"internal", "internal", "retained VM-exec session failed", "", "/root/path", `{}`,
		),
		"unsafe detail type": testFailureBody(
			"internal", "internal", "retained VM-exec session failed", "", "", `{"safe":"value"}`,
		),
		"false reserved marker": testFailureBody(
			"internal", "internal", "retained VM-exec session failed", "", "",
			`{"session_details_omitted":false}`,
		),
		"too many details": testFailureBody(
			"internal", "internal", "retained VM-exec session failed", "", "",
			`{`+strings.Join(detailFields, ",")+`}`,
		),
		"float detail": testFailureBody(
			"validation.failed", "validation", "retained VM-exec session request was invalid", "", "",
			`{"safe":1.5}`,
		),
		"array detail": testFailureBody(
			"validation.failed", "validation", "retained VM-exec session request was invalid", "", "",
			`{"safe":[1]}`,
		),
		"object detail": testFailureBody(
			"validation.failed", "validation", "retained VM-exec session request was invalid", "", "",
			`{"safe":{"nested":1}}`,
		),
		"overflowing integer detail": testFailureBody(
			"validation.failed", "validation", "retained VM-exec session request was invalid", "", "",
			`{"safe":9223372036854775808}`,
		),
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := vmexecsessionwire.DecodeOutcome(t.Context(), bytes.NewReader(testFrame(body)))
			assertValidationError(t, err)
		})
	}
}

func TestFrameWritersHandlePartialAndFailedWrites(t *testing.T) {
	t.Run("partial writes", func(t *testing.T) {
		writer := &partialWriter{maximum: 3}
		require.NoError(t, vmexecsessionwire.EncodeRelease(t.Context(), writer))
		assert.NoError(t, vmexecsessionwire.DecodeRelease(t.Context(), bytes.NewReader(writer.Bytes())))
	})

	t.Run("zero write", func(t *testing.T) {
		err := vmexecsessionwire.EncodeRelease(t.Context(), zeroWriter{})
		assert.ErrorIs(t, err, io.ErrShortWrite)
	})

	t.Run("writer error", func(t *testing.T) {
		sentinel := errors.New("write failed")
		err := vmexecsessionwire.EncodeRelease(t.Context(), errorWriter{err: sentinel})
		assert.ErrorIs(t, err, sentinel)
	})

	t.Run("cancellation between prefix and body", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		writer := &cancelAfterWrite{cancel: cancel}
		err := vmexecsessionwire.EncodeRelease(ctx, writer)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, writer.calls)
	})
}

func TestActionIsFixed(t *testing.T) {
	assert.Equal(t, "jailer-vm-exec-session", vmexecsessionwire.Action)
}

func testFrame(body string) []byte {
	prefix := testPrefix(uint32(len(body)))
	return append(prefix, body...)
}

func testFrameBody(t *testing.T, frame []byte) string {
	t.Helper()
	require.GreaterOrEqual(t, len(frame), 14)
	assert.Equal(t, "MVMXSES1", string(frame[:8]))
	assert.Equal(t, uint16(1), binary.BigEndian.Uint16(frame[8:10]))
	bodyLength := binary.BigEndian.Uint32(frame[10:14])
	require.Equal(t, int(bodyLength), len(frame)-14)
	return string(frame[14:])
}

func testFailureBody(code, class, message, operation, entity, details string) string {
	return `{"schema_version":1,"phase":"failure","error":{"code":"` + code +
		`","class":"` + class + `","message":"` + message + `","operation":"` + operation +
		`","entity":"` + entity + `","details":` + details + `}}`
}

func assertValidationError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
}

type partialWriter struct {
	bytes.Buffer
	maximum int
}

func (writer *partialWriter) Write(value []byte) (int, error) {
	if len(value) > writer.maximum {
		value = value[:writer.maximum]
	}
	return writer.Buffer.Write(value)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

type errorWriter struct {
	err error
}

func (writer errorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

type cancelAfterWrite struct {
	cancel context.CancelFunc
	calls  int
}

func (writer *cancelAfterWrite) Write(value []byte) (int, error) {
	writer.calls++
	writer.cancel()
	return len(value), nil
}

func testPrefix(length uint32) []byte {
	prefix := make([]byte, 14)
	copy(prefix, "MVMXSES1")
	binary.BigEndian.PutUint16(prefix[8:10], 1)
	binary.BigEndian.PutUint32(prefix[10:14], length)
	return prefix
}
