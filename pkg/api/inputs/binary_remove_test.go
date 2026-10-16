package inputs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestBinaryRemoveInputResolveRequiresExactVersionsAndDeduplicates(t *testing.T) {
	tests := map[string]struct {
		input    BinaryRemoveInput
		want     []string
		wantCode string
	}{
		"one exact version": {
			input: BinaryRemoveInput{Versions: []string{"1.16.1"}},
			want:  []string{"1.16.1"},
		},
		"leading v is normalized and duplicates preserve first order": {
			input: BinaryRemoveInput{Versions: []string{"v1.16.1", "1.15.2", "1.16.1"}},
			want:  []string{"1.16.1", "1.15.2"},
		},
		"requires at least one version": {
			input:    BinaryRemoveInput{},
			wantCode: "validation.failed",
		},
		"rejects latest": {
			input:    BinaryRemoveInput{Versions: []string{"latest"}},
			wantCode: "validation.failed",
		},
		"rejects partial version": {
			input:    BinaryRemoveInput{Versions: []string{"1.16"}},
			wantCode: "validation.failed",
		},
		"rejects whitespace instead of guessing": {
			input:    BinaryRemoveInput{Versions: []string{" 1.16.1"}},
			wantCode: "validation.failed",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tc.input.Resolve()
			if tc.wantCode != "" {
				require.Error(t, err)
				var domainErr *errs.DomainError
				require.True(t, errors.As(err, &domainErr))
				assert.Equal(t, tc.wantCode, string(domainErr.Code))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
