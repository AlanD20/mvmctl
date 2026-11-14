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

type cleanupRequest struct {
	VMID string `json:"vm_id"`
}

func TestRequestFrameRoundTrip(t *testing.T) {
	var encoded bytes.Buffer
	err := privilegedwire.EncodeRequest(
		context.Background(),
		&encoded,
		"jailer-cleanup",
		cleanupRequest{VMID: "0123456789abcdef"},
		4096,
	)
	require.NoError(t, err)

	raw := encoded.Bytes()
	require.GreaterOrEqual(t, len(raw), 20)
	assert.Equal(t, []byte("MVMREQ01"), raw[:8])
	assert.Equal(t, uint32(len(raw)-20), binary.BigEndian.Uint32(raw[8:12]))
	assert.Equal(t, uint64(4096), binary.BigEndian.Uint64(raw[12:20]))

	decoded, err := privilegedwire.DecodeRequest[cleanupRequest](
		context.Background(),
		bytes.NewReader(raw),
		"jailer-cleanup",
		4096,
	)
	require.NoError(t, err)
	assert.Equal(t, cleanupRequest{VMID: "0123456789abcdef"}, decoded.Body)
	assert.Equal(t, uint64(4096), decoded.PayloadLength)
}

func TestRequestFrameRejectsInvalidAction(t *testing.T) {
	tests := map[string]string{
		"empty":               "",
		"oversized":           strings.Repeat("a", 65),
		"path":                "jailer/cleanup",
		"whitespace":          "jailer cleanup",
		"non_ascii":           "jailer-cléanup",
		"terminal_byte":       "jailer\ncleanup",
		"leading_punctuation": "-jailer-cleanup",
	}

	for name, action := range tests {
		t.Run(name, func(t *testing.T) {
			var encoded bytes.Buffer
			err := privilegedwire.EncodeRequest(
				context.Background(),
				&encoded,
				action,
				cleanupRequest{VMID: "0123456789abcdef"},
				0,
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
			assert.Empty(t, encoded.Bytes())
		})
	}
}

func TestDecodeRequestRejectsMalformedOrNonCanonicalFrame(t *testing.T) {
	invalidUTF8Header := append(
		[]byte(`{"schema_version":1,"action":"jailer-cleanup","body":{"vm_id":"`),
		0xff,
	)
	invalidUTF8Header = append(invalidUTF8Header, []byte(`"}}`)...)
	tests := map[string]struct {
		frame   []byte
		action  string
		wantErr string
	}{
		"bad_magic": {
			frame:   requestFrame("BADMAGIC", `{"schema_version":1,"action":"jailer-cleanup","body":{"vm_id":"a"}}`, 0),
			action:  "jailer-cleanup",
			wantErr: "magic",
		},
		"zero_header_length": {
			frame:   requestFrameWithLength("MVMREQ01", 0, nil, 0),
			action:  "jailer-cleanup",
			wantErr: "empty",
		},
		"oversized_header_length": {
			frame:   requestFrameWithLength("MVMREQ01", 65537, nil, 0),
			action:  "jailer-cleanup",
			wantErr: "exceeds",
		},
		"payload_exceeds_receiver_limit": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":{"vm_id":"a"}}`,
				1,
			),
			action:  "jailer-cleanup",
			wantErr: "payload length exceeds",
		},
		"truncated_header": {
			frame: requestFrameWithLength(
				"MVMREQ01",
				64,
				[]byte(`{"schema_version":1}`),
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "request header",
		},
		"unsupported_schema": {
			frame:   requestFrame("MVMREQ01", `{"schema_version":2,"action":"jailer-cleanup","body":{"vm_id":"a"}}`, 0),
			action:  "jailer-cleanup",
			wantErr: "schema version",
		},
		"invalid_utf8": {
			frame: requestFrameWithLength(
				"MVMREQ01",
				uint32(len(invalidUTF8Header)),
				invalidUTF8Header,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "UTF-8",
		},
		"action_mismatch": {
			frame:   requestFrame("MVMREQ01", `{"schema_version":1,"action":"jailer-remove","body":{"vm_id":"a"}}`, 0),
			action:  "jailer-cleanup",
			wantErr: "does not match",
		},
		"unknown_envelope_field": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":{"vm_id":"a"},"command":"id"}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "unknown JSON field",
		},
		"case_variant_schema_field": {
			frame: requestFrame(
				"MVMREQ01",
				`{"SCHEMA_VERSION":1,"action":"jailer-cleanup","body":{"vm_id":"a"}}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "unknown JSON field",
		},
		"missing_body": {
			frame:   requestFrame("MVMREQ01", `{"schema_version":1,"action":"jailer-cleanup"}`, 0),
			action:  "jailer-cleanup",
			wantErr: "missing required",
		},
		"null_body": {
			frame:   requestFrame("MVMREQ01", `{"schema_version":1,"action":"jailer-cleanup","body":null}`, 0),
			action:  "jailer-cleanup",
			wantErr: "must not be null",
		},
		"duplicate_envelope_field": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","action":"jailer-remove","body":{"vm_id":"a"}}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "duplicate JSON field",
		},
		"case_folded_duplicate_nested_field": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":{"vm_id":"a","VM_ID":"b"}}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "case-folded duplicate JSON field",
		},
		"single_case_variant_body_field": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":{"VM_ID":"a"}}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "unknown JSON field",
		},
		"unknown_body_field": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":{"vm_id":"a","path":"/root"}}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "unknown field",
		},
		"trailing_header_json": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":{"vm_id":"a"}} {}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "trailing JSON",
		},
		"excessive_depth": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":`+
					strings.Repeat("[", 33)+"0"+strings.Repeat("]", 33)+`}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "nesting exceeds",
		},
		"excessive_object_fields": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":`+objectWithFields(65)+`}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "object exceeds",
		},
		"excessive_array_items": {
			frame: requestFrame(
				"MVMREQ01",
				`{"schema_version":1,"action":"jailer-cleanup","body":[`+
					strings.Repeat(`"a",`, 1024)+`"a"]}`,
				0,
			),
			action:  "jailer-cleanup",
			wantErr: "array exceeds",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := privilegedwire.DecodeRequest[cleanupRequest](
				context.Background(),
				bytes.NewReader(tc.frame),
				tc.action,
				0,
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestRequestFrameHonorsContextAndWriteFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	var encoded bytes.Buffer
	err := privilegedwire.EncodeRequest(
		canceled,
		&encoded,
		"jailer-cleanup",
		cleanupRequest{VMID: "a"},
		0,
	)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, encoded.Bytes())

	err = privilegedwire.EncodeRequest(
		context.Background(),
		shortWriter{},
		"jailer-cleanup",
		cleanupRequest{VMID: "a"},
		0,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, io.ErrShortWrite)
}

func TestEncodeRequestRejectsNullOrOversizedBodyBeforeWriting(t *testing.T) {
	tests := map[string]struct {
		body    *cleanupRequest
		wantErr string
	}{
		"null": {
			wantErr: "must not be null",
		},
		"oversized": {
			body:    &cleanupRequest{VMID: strings.Repeat("a", 65536)},
			wantErr: "exceeds",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var encoded bytes.Buffer
			err := privilegedwire.EncodeRequest(
				context.Background(),
				&encoded,
				"jailer-cleanup",
				tc.body,
				0,
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Empty(t, encoded.Bytes())
		})
	}
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return 0, nil
}

func requestFrame(magic string, header string, payloadLength uint64) []byte {
	return requestFrameWithLength(magic, uint32(len(header)), []byte(header), payloadLength)
}

func requestFrameWithLength(magic string, headerLength uint32, header []byte, payloadLength uint64) []byte {
	frame := make([]byte, 20+len(header))
	copy(frame, magic)
	binary.BigEndian.PutUint32(frame[8:12], headerLength)
	binary.BigEndian.PutUint64(frame[12:20], payloadLength)
	copy(frame[20:], header)
	return frame
}

func objectWithFields(count int) string {
	var value strings.Builder
	value.WriteByte('{')
	for index := range count {
		if index > 0 {
			value.WriteByte(',')
		}
		value.WriteString(fmt.Sprintf(`"field%d":%d`, index, index))
	}
	value.WriteByte('}')
	return value.String()
}
