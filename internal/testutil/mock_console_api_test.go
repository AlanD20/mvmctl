package testutil_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/results"
)

// --- MockConsoleAPI ---
// Rationale: MockConsoleAPI satisfies api.ConsoleAPI for testing upper layers.
// Tests verify default zero returns and custom function routing.

func TestMockConsoleAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockConsoleAPI{}

	t.Run("ConsoleGetState_returns_nil_nil", func(t *testing.T) {
		result, err := m.ConsoleGetState(ctx, "test-vm")
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("ConsoleGetConnectionInfo_returns_nil_nil", func(t *testing.T) {
		result, err := m.ConsoleGetConnectionInfo(ctx, "test-vm")
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("ConsoleKill_returns_nil", func(t *testing.T) {
		err := m.ConsoleKill(ctx, "test-vm")
		assert.NoError(t, err)
	})

	t.Run("ConsoleAttachConsole_returns_nil", func(t *testing.T) {
		err := m.ConsoleAttachConsole(ctx, "/tmp/socket", nil, nil)
		assert.NoError(t, err)
	})
}

func TestMockConsoleAPI_CustomFunc(t *testing.T) {
	t.Run("ConsoleGetState_custom_func", func(t *testing.T) {
		expected := &results.ConsoleStateResult{
			Running:    true,
			PID:        nil,
			SocketPath: "/tmp/vm.sock",
		}
		m := &testutil.MockConsoleAPI{
			ConsoleGetStateFunc: func(ctx context.Context, identifier string) (*results.ConsoleStateResult, error) {
				return expected, nil
			},
		}
		result, err := m.ConsoleGetState(ctx, "test-vm")
		require.NoError(t, err)
		require.NotNil(t, result)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("ConsoleGetState() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("ConsoleAttachConsole_custom_returns_error", func(t *testing.T) {
		m := &testutil.MockConsoleAPI{
			ConsoleAttachConsoleFunc: func(ctx context.Context, socketPath string, stdin io.Reader, stdout io.Writer) error {
				return errors.New("attach failed")
			},
		}
		err := m.ConsoleAttachConsole(ctx, "/tmp/test-sock", nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attach failed")
		return
	})
}
