package privileged

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestIsInvocation_ReservesEveryProtocolMarker(t *testing.T) {
	tests := map[string]struct {
		args []string
		want bool
	}{
		"current_marker":      {args: []string{Marker, "vm-cleanup"}, want: true},
		"unsupported_version": {args: []string{markerPrefix + "2", "vm-cleanup"}, want: true},
		"missing_arguments":   {args: []string{Marker}, want: true},
		"ordinary_cli":        {args: []string{"vm", "list"}},
		"similar_public_name": {args: []string{"mvm-privileged-v1"}},
		"empty":               {},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsInvocation(tc.args))
		})
	}
}

func TestParseInvocation_FailsClosed(t *testing.T) {
	tests := map[string]struct {
		args    []string
		wantErr string
	}{
		"unsupported_version": {
			args:    []string{markerPrefix + "2", "vm-cleanup"},
			wantErr: "upgrade /usr/local/bin/mvm",
		},
		"missing_action": {
			args:    []string{Marker},
			wantErr: "requires exactly one action",
		},
		"empty_action": {
			args:    []string{Marker, ""},
			wantErr: "action is required",
		},
		"extra_argument": {
			args:    []string{Marker, "vm-cleanup", "public-command"},
			wantErr: "requires exactly one action",
		},
		"oversized_action": {
			args:    []string{Marker, strings.Repeat("a", maxActionBytes+1)},
			wantErr: "action exceeds",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseInvocation(tc.args)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestDecodeRequest_RejectsUntrustedJSON(t *testing.T) {
	type nested struct {
		Label string `json:"label"`
	}
	type request struct {
		VMID   string `json:"vm_id"`
		Nested nested `json:"nested"`
	}

	tests := map[string]struct {
		body    string
		wantErr string
	}{
		"empty": {
			wantErr: "request body is required",
		},
		"malformed": {
			body:    `{"vm_id":`,
			wantErr: "decode privileged request",
		},
		"unknown_field": {
			body:    `{"vm_id":"abc","nested":{"label":"ok"},"command":"id"}`,
			wantErr: "unknown field",
		},
		"duplicate_top_level_field": {
			body:    `{"vm_id":"first","vm_id":"second","nested":{"label":"ok"}}`,
			wantErr: `duplicate JSON field "vm_id"`,
		},
		"duplicate_nested_field": {
			body:    `{"vm_id":"abc","nested":{"label":"first","label":"second"}}`,
			wantErr: `duplicate JSON field "label"`,
		},
		"case_folded_duplicate_top_level_field": {
			body:    `{"vm_id":"first","VM_ID":"second","nested":{"label":"ok"}}`,
			wantErr: `case-folded duplicate JSON field "VM_ID" conflicts with "vm_id"`,
		},
		"case_folded_duplicate_nested_field": {
			body:    `{"vm_id":"abc","nested":{"label":"first","LABEL":"second"}}`,
			wantErr: `case-folded duplicate JSON field "LABEL" conflicts with "label"`,
		},
		"trailing_json": {
			body:    `{"vm_id":"abc","nested":{"label":"ok"}} {}`,
			wantErr: "unexpected trailing JSON",
		},
		"oversized": {
			body:    strings.Repeat(" ", maxRequestBytes+1),
			wantErr: "request body exceeds",
		},
		"excessive_nesting": {
			body:    strings.Repeat("[", maxJSONDepth+1) + "0" + strings.Repeat("]", maxJSONDepth+1),
			wantErr: "JSON nesting exceeds",
		},
		"excessive_array_items": {
			body:    "[" + strings.Repeat("0,", maxJSONArrayItems) + "0]",
			wantErr: "JSON array exceeds",
		},
		"excessive_object_fields": {
			body:    objectWithFields(maxJSONObjectFields + 1),
			wantErr: "JSON object exceeds",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeRequest[request](strings.NewReader(tc.body))
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestDecodeRequest_AcceptsOneExactObject(t *testing.T) {
	type request struct {
		VMID string `json:"vm_id"`
	}

	got, err := decodeRequest[request](strings.NewReader("{\"vm_id\":\"abc\"}\n"))
	require.NoError(t, err)
	assert.Equal(t, "abc", got.VMID)
}

func objectWithFields(count int) string {
	var body strings.Builder
	body.WriteByte('{')
	for index := range count {
		if index > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`"field`)
		body.WriteString(fmt.Sprintf("%d", index))
		body.WriteString(`":0`)
	}
	body.WriteByte('}')
	return body.String()
}
