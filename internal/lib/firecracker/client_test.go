package firecracker

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- isConnRefused ---
// Rationale: Connection-refused detection drives the Firecracker client
// retry loop (5 retries with exponential backoff). A false negative drops
// retries prematurely; a false positive retries on unrelated errors,
// delaying failure reporting.
// Note: This test is in an internal package (package firecracker, not
// firecracker_test) because isConnRefused is unexported. External test
// packages cannot access it.

func TestIsConnRefused(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		// Error/boundary cases first
		"other_syscall_error": {err: os.NewSyscallError("connect", syscall.ECONNRESET), want: false},
		"non_syscall_error":   {err: errors.New("something went wrong"), want: false},
		"nil":                 {err: nil, want: false},

		// Happy paths
		"econnrefused": {err: os.NewSyscallError("connect", syscall.ECONNREFUSED), want: true},
		"wrapped_econnrefused": {
			err:  fmt.Errorf("api call failed: %w", os.NewSyscallError("connect", syscall.ECONNREFUSED)),
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := isConnRefused(tc.err)
			assert.Equal(t, tc.want, got)
		})
	}
}
