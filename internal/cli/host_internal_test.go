package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/pkg/errs"
)

func TestSystemInstallPartialWarningDescribesKnownFailure(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		wantContains    string
		wantNotContains string
	}{
		{
			name: "generic post-replacement failure",
			err: errs.New(
				errs.CodeHostInitFailed,
				"close retained descriptor",
				errs.WithDetails(map[string]any{"system_binary_replaced": true}),
			),
			wantContains:    "final completion checks failed",
			wantNotContains: "durability",
		},
		{
			name: "directory durability failure",
			err: errs.New(
				errs.CodeHostInitFailed,
				"sync directory",
				errs.WithDetails(map[string]any{"durability_uncertain": true}),
			),
			wantContains: "durability could not be confirmed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warning := systemInstallPartialWarning(tt.err)
			assert.Contains(t, warning, tt.wantContains)
			if tt.wantNotContains != "" {
				assert.NotContains(t, warning, tt.wantNotContains)
			}
		})
	}
}
