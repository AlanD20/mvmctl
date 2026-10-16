package privilegedwire_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/pkg/errs"
)

type cleanupResult struct {
	Removed bool `json:"removed"`
}

func TestSuccessResponseFrameRoundTrip(t *testing.T) {
	var encoded bytes.Buffer
	err := privilegedwire.EncodeSuccess(
		context.Background(),
		&encoded,
		"jailer-cleanup",
		cleanupResult{Removed: true},
	)
	require.NoError(t, err)

	raw := encoded.Bytes()
	require.GreaterOrEqual(t, len(raw), 12)
	assert.Equal(t, []byte("MVMRES01"), raw[:8])
	assert.Equal(t, uint32(len(raw)-12), binary.BigEndian.Uint32(raw[8:12]))

	outcome, err := privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(raw),
		"jailer-cleanup",
	)
	require.NoError(t, err)
	assert.Equal(t, cleanupResult{Removed: true}, outcome.Result())
	assert.Nil(t, outcome.OperationError())
}

func TestDecodeResponseDistinguishesOperationErrorsFromProtocolFailures(t *testing.T) {
	validError := `{"schema_version":1,"action":"jailer-cleanup","status":"error","error":` +
		`{"code":"vm.cleanup.failed","class":"internal","message":"failed",` +
		`"operation":"vm.cleanup","entity":"vm-id","details":{}}}`
	outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(responseFrame("MVMRES01", validError)),
		"jailer-cleanup",
	)
	require.NoError(t, protocolErr)
	require.NotNil(t, outcome.OperationError())
	assert.Equal(t, errs.Code("vm.cleanup.failed"), outcome.OperationError().Code)
	assert.Zero(t, outcome.Result())

	outcome, protocolErr = privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(responseFrame("BADMAGIC", validError)),
		"jailer-cleanup",
	)
	require.Error(t, protocolErr)
	assert.Nil(t, outcome.OperationError())
	assert.Zero(t, outcome.Result())
}

func TestDecodeResponseRejectsMalformedOrNonCanonicalFrame(t *testing.T) {
	validSuccess := `{"schema_version":1,"action":"jailer-cleanup","status":"success","result":{"removed":true}}`
	validError := `{"schema_version":1,"action":"jailer-cleanup","status":"error","error":` +
		`{"code":"vm.cleanup.failed","class":"internal","message":"failed",` +
		`"operation":"vm.cleanup","entity":"vm-id","details":{}}}`
	tests := map[string]struct {
		frame   []byte
		wantErr string
	}{
		"bad_magic": {
			frame:   responseFrame("BADMAGIC", validSuccess),
			wantErr: "magic",
		},
		"zero_length": {
			frame:   responseFrameWithLength("MVMRES01", 0, nil),
			wantErr: "empty",
		},
		"oversized_length": {
			frame:   responseFrameWithLength("MVMRES01", 65537, nil),
			wantErr: "exceeds",
		},
		"truncated_body": {
			frame:   responseFrameWithLength("MVMRES01", 100, []byte(validSuccess)),
			wantErr: "response body",
		},
		"short_declared_length": {
			frame:   responseFrameWithLength("MVMRES01", 2, []byte(validSuccess)),
			wantErr: "trailing input",
		},
		"trailing_frame_input": {
			frame:   append(responseFrame("MVMRES01", validSuccess), 0),
			wantErr: "trailing input",
		},
		"unsupported_schema": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":2,"action":"jailer-cleanup","status":"success","result":{"removed":true}}`,
			),
			wantErr: "schema version",
		},
		"action_mismatch": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-remove","status":"success","result":{"removed":true}}`,
			),
			wantErr: "does not match",
		},
		"invalid_status": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"ok","result":{"removed":true}}`,
			),
			wantErr: "response status",
		},
		"missing_status": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","result":{"removed":true}}`,
			),
			wantErr: "missing required",
		},
		"unknown_envelope_field": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success",`+
					`"result":{"removed":true},"stderr":"root secret"}`,
			),
			wantErr: "unknown JSON field",
		},
		"duplicate_status": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success",`+
					`"status":"error","result":{"removed":true}}`,
			),
			wantErr: "duplicate JSON field",
		},
		"case_folded_duplicate_result": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success",`+
					`"result":{"removed":true,"REMOVED":false}}`,
			),
			wantErr: "case-folded duplicate JSON field",
		},
		"single_case_variant_result_field": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success",`+
					`"result":{"REMOVED":true}}`,
			),
			wantErr: "unknown JSON field",
		},
		"trailing_json": {
			frame:   responseFrame("MVMRES01", validSuccess+` {}`),
			wantErr: "trailing JSON",
		},
		"null_success_result": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success","result":null}`,
			),
			wantErr: "must not be null",
		},
		"unknown_success_result_field": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success",`+
					`"result":{"removed":true,"command":"id"}}`,
			),
			wantErr: "unknown field",
		},
		"success_with_error": {
			frame: responseFrame(
				"MVMRES01",
				strings.Replace(validError, `"status":"error"`, `"status":"success","result":{"removed":true}`, 1),
			),
			wantErr: "only one result",
		},
		"error_with_result": {
			frame: responseFrame(
				"MVMRES01",
				strings.Replace(validError, `"status":"error"`, `"status":"error","result":{"removed":true}`, 1),
			),
			wantErr: "only one error",
		},
		"error_without_error": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"error"}`,
			),
			wantErr: "only one error",
		},
		"excessive_depth": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success","result":`+
					strings.Repeat("[", 33)+"0"+strings.Repeat("]", 33)+`}`,
			),
			wantErr: "nesting exceeds",
		},
		"excessive_object_fields": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success","result":`+
					objectWithFields(65)+`}`,
			),
			wantErr: "object exceeds",
		},
		"excessive_array_items": {
			frame: responseFrame(
				"MVMRES01",
				`{"schema_version":1,"action":"jailer-cleanup","status":"success","result":[`+
					strings.Repeat(`"a",`, 1024)+`"a"]}`,
			),
			wantErr: "array exceeds",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := privilegedwire.DecodeResponse[cleanupResult](
				context.Background(),
				bytes.NewReader(tc.frame),
				"jailer-cleanup",
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestEncodeSuccessRejectsNullOrOversizedResultBeforeWriting(t *testing.T) {
	type largeResult struct {
		Value string `json:"value"`
	}
	tests := map[string]struct {
		result  *largeResult
		wantErr string
	}{
		"null": {
			wantErr: "must not be null",
		},
		"oversized": {
			result:  &largeResult{Value: strings.Repeat("a", 65536)},
			wantErr: "exceeds",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var encoded bytes.Buffer
			err := privilegedwire.EncodeSuccess(
				context.Background(),
				&encoded,
				"jailer-cleanup",
				tc.result,
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Empty(t, encoded.Bytes())
		})
	}
}

func TestResponseFrameHonorsContextAndWriteFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	var encoded bytes.Buffer
	err := privilegedwire.EncodeSuccess(
		canceled,
		&encoded,
		"jailer-cleanup",
		cleanupResult{Removed: true},
	)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, encoded.Bytes())

	err = privilegedwire.EncodeError(
		context.Background(),
		shortWriter{},
		"jailer-cleanup",
		errs.New(errs.CodeProcessError, "operation failed"),
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, io.ErrShortWrite)

	_, err = privilegedwire.DecodeResponse[cleanupResult](
		canceled,
		bytes.NewReader(responseFrame(
			"MVMRES01",
			`{"schema_version":1,"action":"jailer-cleanup","status":"success","result":{"removed":true}}`,
		)),
		"jailer-cleanup",
	)
	require.ErrorIs(t, err, context.Canceled)
}

func responseFrame(magic string, body string) []byte {
	return responseFrameWithLength(magic, uint32(len(body)), []byte(body))
}

func responseFrameWithLength(magic string, bodyLength uint32, body []byte) []byte {
	frame := make([]byte, 12+len(body))
	copy(frame, magic)
	binary.BigEndian.PutUint32(frame[8:12], bodyLength)
	copy(frame[12:], body)
	return frame
}

func errorResponseBody(errorJSON string) string {
	return fmt.Sprintf(
		`{"schema_version":1,"action":"jailer-cleanup","status":"error","error":%s}`,
		errorJSON,
	)
}
