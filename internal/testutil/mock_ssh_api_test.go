package testutil_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

// ─── MockSSHAPI ───────────────────────────────────────────────────────────────
// Rationale: MockSSHAPI satisfies api.SSHAPI for testing upper layers.
// SSHConnect is the only method; tests verify default return and custom routing.

func TestMockSSHAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockSSHAPI{}

	err := m.SSHConnect(ctx, inputs.SSHInput{}, nil)
	assert.NoError(t, err)
}

func TestMockSSHAPI_CustomFunc(t *testing.T) {
	t.Run("custom_returns_nil", func(t *testing.T) {
		m := &testutil.MockSSHAPI{
			SSHConnectFunc: func(ctx context.Context, input inputs.SSHInput, onProgress event.OnProgressCallback) error {
				return nil
			},
		}
		err := m.SSHConnect(ctx, inputs.SSHInput{}, nil)
		assert.NoError(t, err)
	})

	t.Run("custom_returns_error", func(t *testing.T) {
		m := &testutil.MockSSHAPI{
			SSHConnectFunc: func(ctx context.Context, input inputs.SSHInput, onProgress event.OnProgressCallback) error {
				return errors.New("ssh connection failed")
			},
		}
		err := m.SSHConnect(ctx, inputs.SSHInput{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ssh connection failed")
		return
	})
}
