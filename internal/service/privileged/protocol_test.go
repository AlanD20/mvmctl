package privileged

import (
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
			wantErr: "1-64 byte ASCII identifier",
		},
		"extra_argument": {
			args:    []string{Marker, "vm-cleanup", "public-command"},
			wantErr: "requires exactly one action",
		},
		"oversized_action": {
			args:    []string{Marker, strings.Repeat("a", maxActionBytes+1)},
			wantErr: "1-64 byte ASCII identifier",
		},
		"leading_punctuation": {
			args:    []string{Marker, "-vm-cleanup"},
			wantErr: "1-64 byte ASCII identifier",
		},
		"whitespace": {
			args:    []string{Marker, "vm cleanup"},
			wantErr: "1-64 byte ASCII identifier",
		},
		"slash": {
			args:    []string{Marker, "vm/cleanup"},
			wantErr: "1-64 byte ASCII identifier",
		},
		"non_ascii": {
			args:    []string{Marker, "vm-cleanup-é"},
			wantErr: "1-64 byte ASCII identifier",
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

func TestParseInvocation_AcceptsOneASCIIIdentifier(t *testing.T) {
	got, err := parseInvocation([]string{Marker, "a.Z_9-x"})
	require.NoError(t, err)
	assert.Equal(t, "a.Z_9-x", got.action)
}
