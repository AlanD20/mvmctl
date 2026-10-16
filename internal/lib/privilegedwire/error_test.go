package privilegedwire_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/pkg/errs"
)

func TestDomainErrorResponsePreservesSafeAuthorityFields(t *testing.T) {
	source := &errs.DomainError{
		Code:    errs.Code("vm.cleanup.partial"),
		Class:   errs.ClassConflict,
		Message: "cleanup changed state\nretry after inspection\tcarefully",
		Op:      "vm.cleanup",
		Entity:  "0123456789abcdef",
		Err:     errors.New("secret wrapped stderr and stack material"),
		Details: map[string]any{
			"record_replaced":    true,
			"cleanup_generation": int64(7),
			"phase":              "mounts",
			"resources":          []string{"rootfs", "volume-1"},
		},
	}

	var encoded bytes.Buffer
	err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
	require.NoError(t, err)
	assert.NotContains(t, encoded.String(), "secret wrapped stderr")

	outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(encoded.Bytes()),
		"jailer-cleanup",
	)
	require.NoError(t, protocolErr)
	decoded := outcome.OperationError()
	require.NotNil(t, decoded)
	assert.Equal(t, source.Code, decoded.Code)
	assert.Equal(t, source.Class, decoded.Class)
	assert.Equal(t, source.Message, decoded.Message)
	assert.Equal(t, source.Op, decoded.Op)
	assert.Equal(t, source.Entity, decoded.Entity)
	assert.Nil(t, decoded.Err)
	assert.Equal(t, source.Details, decoded.Details)
}

func TestDomainErrorResponseUsesStableClassStrings(t *testing.T) {
	tests := map[string]struct {
		class errs.Class
		wire  string
	}{
		"unknown":           {class: errs.ClassUnknown, wire: "unknown"},
		"validation":        {class: errs.ClassValidation, wire: "validation"},
		"conflict":          {class: errs.ClassConflict, wire: "conflict"},
		"retryable":         {class: errs.ClassRetryable, wire: "retryable"},
		"internal":          {class: errs.ClassInternal, wire: "internal"},
		"needs_interaction": {class: errs.ClassNeedsInteraction, wire: "needs_interaction"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			source := &errs.DomainError{
				Code:    errs.CodeProcessError,
				Class:   tc.class,
				Message: "operation failed",
				Op:      "vm.cleanup",
				Details: map[string]any{},
			}
			var encoded bytes.Buffer
			err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
			require.NoError(t, err)
			assert.Contains(t, encoded.String(), `"class":"`+tc.wire+`"`)

			outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
				context.Background(),
				bytes.NewReader(encoded.Bytes()),
				"jailer-cleanup",
			)
			require.NoError(t, protocolErr)
			require.NotNil(t, outcome.OperationError())
			assert.Equal(t, tc.class, outcome.OperationError().Class)
		})
	}
}

func TestDomainErrorResponseOmitsUnsupportedDetailsVisibly(t *testing.T) {
	source := &errs.DomainError{
		Code:    errs.CodeProcessError,
		Class:   errs.ClassInternal,
		Message: "operation failed",
		Op:      "vm.cleanup",
		Details: map[string]any{
			"safe":        true,
			"float":       1.5,
			"null":        nil,
			"nested":      map[string]string{"command": "id"},
			"integer_set": []int{1, 2},
			"stderr":      "secret subprocess output",
			"stack":       "secret stack trace",
		},
	}
	var encoded bytes.Buffer
	err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
	require.NoError(t, err)
	assert.NotContains(t, encoded.String(), "secret subprocess output")
	assert.NotContains(t, encoded.String(), "secret stack trace")

	outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(encoded.Bytes()),
		"jailer-cleanup",
	)
	require.NoError(t, protocolErr)
	require.NotNil(t, outcome.OperationError())
	details := outcome.OperationError().Details
	assert.Equal(t, true, details["safe"])
	assert.Equal(t, true, details["privileged_details_omitted"])
	assert.Len(t, details, 2)
}

func TestDomainErrorResponseOmitsSensitiveDetailTokens(t *testing.T) {
	tests := map[string]string{
		"raw_stderr":       "raw_stderr",
		"stderr_excerpt":   "stderr_excerpt",
		"process_stdout":   "process.stdout",
		"wrapped_cause":    "wrapped-cause",
		"nested_error":     "process_error_message",
		"stack_token":      "handler_stack_trace",
		"stacktrace_token": "HANDLER.STACKTRACE",
		"err_token":        "wrapped_err_text",
	}

	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			secret := "secret-" + name
			source := &errs.DomainError{
				Code:    errs.CodeProcessError,
				Class:   errs.ClassInternal,
				Message: "operation failed",
				Op:      "vm.cleanup",
				Details: map[string]any{
					"record_replaced": true,
					key:               secret,
				},
			}
			var encoded bytes.Buffer
			err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
			require.NoError(t, err)
			assert.NotContains(t, encoded.String(), secret)

			outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
				context.Background(),
				bytes.NewReader(encoded.Bytes()),
				"jailer-cleanup",
			)
			require.NoError(t, protocolErr)
			require.NotNil(t, outcome.OperationError())
			details := outcome.OperationError().Details
			assert.Equal(t, true, details["record_replaced"])
			assert.Equal(t, true, details["privileged_details_omitted"])
			assert.Len(t, details, 2)
		})
	}
}

func TestDomainErrorResponsePreservesPartialStateDetailKeys(t *testing.T) {
	details := map[string]any{
		"record_replaced":      true,
		"durability_uncertain": true,
		"process_started":      true,
		"outcome_unknown":      true,
		"cleanup_generation":   int64(4),
	}
	source := &errs.DomainError{
		Code:    errs.CodeProcessError,
		Class:   errs.ClassInternal,
		Message: "operation failed",
		Op:      "vm.cleanup",
		Details: details,
	}

	var encoded bytes.Buffer
	err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
	require.NoError(t, err)
	outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(encoded.Bytes()),
		"jailer-cleanup",
	)
	require.NoError(t, protocolErr)
	require.NotNil(t, outcome.OperationError())
	assert.Equal(t, details, outcome.OperationError().Details)
}

func TestDomainErrorResponseOmitsInvalidDetailKeys(t *testing.T) {
	tests := map[string]string{
		"space_separator_bypass": "stderr excerpt",
		"non_ascii":              "phase_é",
	}

	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			secret := "secret-" + name
			source := &errs.DomainError{
				Code:    errs.CodeProcessError,
				Class:   errs.ClassInternal,
				Message: "operation failed",
				Op:      "vm.cleanup",
				Details: map[string]any{
					"record_replaced": true,
					key:               secret,
				},
			}
			var encoded bytes.Buffer
			err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
			require.NoError(t, err)
			assert.NotContains(t, encoded.String(), secret)

			outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
				context.Background(),
				bytes.NewReader(encoded.Bytes()),
				"jailer-cleanup",
			)
			require.NoError(t, protocolErr)
			require.NotNil(t, outcome.OperationError())
			details := outcome.OperationError().Details
			assert.Equal(t, true, details["record_replaced"])
			assert.Equal(t, true, details["privileged_details_omitted"])
			assert.Len(t, details, 2)
		})
	}
}

func TestDecodeDomainErrorRejectsSensitiveDetailTokens(t *testing.T) {
	tests := map[string]string{
		"raw_stderr":       "raw_stderr",
		"stderr_excerpt":   "stderr_excerpt",
		"process_stdout":   "process.stdout",
		"wrapped_cause":    "wrapped-cause",
		"nested_error":     "process_error_message",
		"stack_token":      "handler_stack_trace",
		"stacktrace_token": "HANDLER.STACKTRACE",
		"err_token":        "wrapped_err_text",
	}

	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			errorJSON := `{"code":"vm.cleanup.failed","class":"internal","message":"failed",` +
				`"operation":"vm.cleanup","entity":"vm-id","details":{"` + key + `":"secret"}}`
			frame := responseFrame("MVMRES01", errorResponseBody(errorJSON))
			_, err := privilegedwire.DecodeResponse[cleanupResult](
				context.Background(),
				bytes.NewReader(frame),
				"jailer-cleanup",
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, "detail key")
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestDomainErrorResponsePreservesMaximumBoundedDetailSet(t *testing.T) {
	details := make(map[string]any, 32)
	for index := range 32 {
		details[fmt.Sprintf("detail_%02d", index)] = int64(index)
	}
	source := &errs.DomainError{
		Code:    errs.CodeProcessError,
		Class:   errs.ClassInternal,
		Message: "operation failed",
		Op:      "vm.cleanup",
		Details: details,
	}

	var encoded bytes.Buffer
	err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
	require.NoError(t, err)
	outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(encoded.Bytes()),
		"jailer-cleanup",
	)
	require.NoError(t, protocolErr)
	decoded := outcome.OperationError()
	require.NotNil(t, decoded)
	assert.Equal(t, details, decoded.Details)
	_, omitted := decoded.Details["privileged_details_omitted"]
	assert.False(t, omitted)
}

func TestDomainErrorResponseBoundsExcessDetailCountVisibly(t *testing.T) {
	details := make(map[string]any, 33)
	for index := range 33 {
		details[fmt.Sprintf("detail_%02d", index)] = int64(index)
	}
	source := &errs.DomainError{
		Code:    errs.CodeProcessError,
		Class:   errs.ClassInternal,
		Message: "operation failed",
		Op:      "vm.cleanup",
		Details: details,
	}

	var encoded bytes.Buffer
	err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
	require.NoError(t, err)
	outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(encoded.Bytes()),
		"jailer-cleanup",
	)
	require.NoError(t, protocolErr)
	decoded := outcome.OperationError()
	require.NotNil(t, decoded)
	assert.Len(t, decoded.Details, 32)
	assert.Equal(t, true, decoded.Details["privileged_details_omitted"])
	for index := range 31 {
		assert.Equal(t, int64(index), decoded.Details[fmt.Sprintf("detail_%02d", index)])
	}
	assert.NotContains(t, decoded.Details, "detail_31")
	assert.NotContains(t, decoded.Details, "detail_32")
}

func TestDomainErrorResponseBoundsWorkForLargeDetailMap(t *testing.T) {
	details := make(map[string]any, 10_000)
	for index := range 10_000 {
		details[fmt.Sprintf("detail_%05d", index)] = true
	}
	source := &errs.DomainError{
		Code:    errs.CodeProcessError,
		Class:   errs.ClassInternal,
		Message: "operation failed",
		Op:      "vm.cleanup",
		Details: details,
	}
	benchmark := testing.Benchmark(func(benchmark *testing.B) {
		benchmark.ReportAllocs()
		for range benchmark.N {
			var encoded bytes.Buffer
			err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
			if err != nil {
				benchmark.Fatal(err)
			}
		}
	})
	allocatedBytes := benchmark.AllocedBytesPerOp()
	t.Logf("large detail normalization allocated %d bytes per operation", allocatedBytes)
	assert.Less(t, allocatedBytes, int64(64*1024))

	var encodeErr error
	allocations := testing.AllocsPerRun(1, func() {
		var encoded bytes.Buffer
		encodeErr = privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
	})
	require.NoError(t, encodeErr)
	assert.Less(t, allocations, 1000.0)
}

func TestDomainErrorResponseRejectsUnicodeFormattingControls(t *testing.T) {
	source := &errs.DomainError{
		Code:    errs.CodeProcessError,
		Class:   errs.ClassInternal,
		Message: "operation failed\u202esecret",
		Op:      "vm.cleanup",
		Details: map[string]any{},
	}

	var encoded bytes.Buffer
	err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
	require.NoError(t, err)
	assert.NotContains(t, encoded.String(), "secret")

	outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
		context.Background(),
		bytes.NewReader(encoded.Bytes()),
		"jailer-cleanup",
	)
	require.NoError(t, protocolErr)
	decoded := outcome.OperationError()
	require.NotNil(t, decoded)
	assert.Equal(t, errs.CodeInternal, decoded.Code)
	assert.Equal(t, "privileged operation outcome could not be encoded", decoded.Message)
}

func TestDomainErrorResponseFallsBackWhenAuthorityFieldsCannotFit(t *testing.T) {
	hugeDetails := make(map[string]any, 31)
	for index := range 31 {
		hugeDetails[fmt.Sprintf("detail_%02d", index)] = strings.Repeat("a", 4096)
	}
	tests := map[string]error{
		"non_domain_error": errors.New("raw stderr must not cross the wire"),
		"nil_error":        nil,
		"invalid_code": &errs.DomainError{
			Code: errs.Code("invalid/code"), Class: errs.ClassInternal, Message: "failed",
		},
		"invalid_class": &errs.DomainError{
			Code: errs.CodeInternal, Class: errs.Class(99), Message: "failed",
		},
		"oversized_message": &errs.DomainError{
			Code: errs.CodeInternal, Class: errs.ClassInternal, Message: strings.Repeat("a", 8193),
		},
		"terminal_control": &errs.DomainError{
			Code: errs.CodeInternal, Class: errs.ClassInternal, Message: "failed\x1b[31m",
		},
		"frame_too_large": &errs.DomainError{
			Code: errs.CodeInternal, Class: errs.ClassInternal, Message: "failed", Details: hugeDetails,
		},
	}

	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			var encoded bytes.Buffer
			err := privilegedwire.EncodeError(context.Background(), &encoded, "jailer-cleanup", source)
			require.NoError(t, err)
			assert.NotContains(t, encoded.String(), "raw stderr")

			outcome, protocolErr := privilegedwire.DecodeResponse[cleanupResult](
				context.Background(),
				bytes.NewReader(encoded.Bytes()),
				"jailer-cleanup",
			)
			require.NoError(t, protocolErr)
			decoded := outcome.OperationError()
			require.NotNil(t, decoded)
			assert.Equal(t, errs.CodeInternal, decoded.Code)
			assert.Equal(t, errs.ClassInternal, decoded.Class)
			assert.Equal(t, "privileged operation outcome could not be encoded", decoded.Message)
			assert.Equal(t, "privileged", decoded.Op)
			assert.Empty(t, decoded.Entity)
			assert.Equal(t, map[string]any{"outcome_unknown": true}, decoded.Details)
		})
	}
}

func TestDomainErrorFallbackIsDeterministic(t *testing.T) {
	var first bytes.Buffer
	err := privilegedwire.EncodeError(
		context.Background(),
		&first,
		"jailer-cleanup",
		errors.New("first unsafe cause"),
	)
	require.NoError(t, err)

	var second bytes.Buffer
	err = privilegedwire.EncodeError(
		context.Background(),
		&second,
		"jailer-cleanup",
		errors.New("different unsafe cause"),
	)
	require.NoError(t, err)
	assert.Equal(t, first.Bytes(), second.Bytes())
}

func TestDecodeDomainErrorRejectsUnsafeWireValues(t *testing.T) {
	base := `{"code":"vm.cleanup.failed","class":"internal","message":"failed",` +
		`"operation":"vm.cleanup","entity":"vm-id","details":{}}`
	tests := map[string]struct {
		errorJSON string
		wantErr   string
	}{
		"unknown_error_field": {
			errorJSON: strings.Replace(base, `"details":{}`, `"details":{},"stderr":"secret"`, 1),
			wantErr:   "unknown JSON field",
		},
		"missing_error_field": {
			errorJSON: `{"code":"vm.cleanup.failed","class":"internal","message":"failed",` +
				`"operation":"vm.cleanup","entity":"vm-id"}`,
			wantErr: "missing required",
		},
		"duplicate_nested_error_field": {
			errorJSON: strings.Replace(base, `"code":"vm.cleanup.failed"`,
				`"code":"vm.cleanup.failed","code":"vm.other"`, 1),
			wantErr: "duplicate JSON field",
		},
		"invalid_code": {
			errorJSON: strings.Replace(base, "vm.cleanup.failed", "vm/cleanup", 1),
			wantErr:   "error code",
		},
		"oversized_code": {
			errorJSON: strings.Replace(base, "vm.cleanup.failed", strings.Repeat("a", 129), 1),
			wantErr:   "error code",
		},
		"wrong_class": {
			errorJSON: strings.Replace(base, `"class":"internal"`, `"class":"fatal"`, 1),
			wantErr:   "error class",
		},
		"message_control": {
			errorJSON: strings.Replace(base, `"message":"failed"`, `"message":"failed\u001b"`, 1),
			wantErr:   "error message",
		},
		"message_unicode_format_control": {
			errorJSON: strings.Replace(base, `"message":"failed"`, `"message":"failed\u202e"`, 1),
			wantErr:   "error message",
		},
		"oversized_message": {
			errorJSON: strings.Replace(
				base,
				`"message":"failed"`,
				`"message":"`+strings.Repeat("a", 8193)+`"`,
				1,
			),
			wantErr: "error message",
		},
		"operation_control": {
			errorJSON: strings.Replace(
				base,
				`"operation":"vm.cleanup"`,
				`"operation":"vm.cleanup\n"`,
				1,
			),
			wantErr: "error operation",
		},
		"oversized_operation": {
			errorJSON: strings.Replace(
				base,
				`"operation":"vm.cleanup"`,
				`"operation":"`+strings.Repeat("a", 129)+`"`,
				1,
			),
			wantErr: "error operation",
		},
		"oversized_entity": {
			errorJSON: strings.Replace(base, "vm-id", strings.Repeat("a", 1025), 1),
			wantErr:   "error entity",
		},
		"details_null": {
			errorJSON: strings.Replace(base, `"details":{}`, `"details":{"bad":null}`, 1),
			wantErr:   "detail value",
		},
		"omitted_flag_must_be_true": {
			errorJSON: strings.Replace(
				base,
				`"details":{}`,
				`"details":{"privileged_details_omitted":false}`,
				1,
			),
			wantErr: "omission flag",
		},
		"details_float": {
			errorJSON: strings.Replace(base, `"details":{}`, `"details":{"bad":1.25}`, 1),
			wantErr:   "integer detail",
		},
		"details_integer_overflow": {
			errorJSON: strings.Replace(
				base,
				`"details":{}`,
				`"details":{"bad":9223372036854775808}`,
				1,
			),
			wantErr: "integer detail",
		},
		"detail_key_oversized": {
			errorJSON: strings.Replace(
				base,
				`"details":{}`,
				`"details":{"`+strings.Repeat("a", 65)+`":true}`,
				1,
			),
			wantErr: "detail key",
		},
		"detail_key_space_separator": {
			errorJSON: strings.Replace(
				base,
				`"details":{}`,
				`"details":{"stderr excerpt":"secret"}`,
				1,
			),
			wantErr: "detail key",
		},
		"detail_key_non_ascii": {
			errorJSON: strings.Replace(base, `"details":{}`, `"details":{"phase_é":true}`, 1),
			wantErr:   "detail key",
		},
		"details_nested": {
			errorJSON: strings.Replace(base, `"details":{}`, `"details":{"bad":{"command":"id"}}`, 1),
			wantErr:   "detail value",
		},
		"details_arbitrary_array": {
			errorJSON: strings.Replace(base, `"details":{}`, `"details":{"bad":[1,2]}`, 1),
			wantErr:   "string array",
		},
		"details_too_many": {
			errorJSON: strings.Replace(base, `"details":{}`, `"details":`+objectWithFields(33), 1),
			wantErr:   "exceed 32 entries",
		},
		"detail_string_oversized": {
			errorJSON: strings.Replace(
				base,
				`"details":{}`,
				`"details":{"bad":"`+strings.Repeat("a", 4097)+`"}`,
				1,
			),
			wantErr: "error detail",
		},
		"detail_array_too_many": {
			errorJSON: strings.Replace(
				base,
				`"details":{}`,
				`"details":{"bad":[`+strings.Repeat(`"a",`, 32)+`"a"]}`,
				1,
			),
			wantErr: "array exceeds 32 items",
		},
		"detail_array_item_oversized": {
			errorJSON: strings.Replace(
				base,
				`"details":{}`,
				`"details":{"bad":["`+strings.Repeat("a", 1025)+`"]}`,
				1,
			),
			wantErr: "array item",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			frame := responseFrame("MVMRES01", errorResponseBody(tc.errorJSON))
			_, err := privilegedwire.DecodeResponse[cleanupResult](
				context.Background(),
				bytes.NewReader(frame),
				"jailer-cleanup",
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}
