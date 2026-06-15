package testutil_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// Compile-time check that *MockOperation satisfies the api.API interface.
var _ api.API = (*testutil.MockOperation)(nil)

// ─── MockOperation ─────────────────────────────────────────────────────────────
// Rationale: MockOperation is the composite mock that embeds all per-domain
// mocks and satisfies api.API. Tests verify that embedded mocks route through
// correctly.

func TestMockOperation_EmbeddedVMAPI(t *testing.T) {
	m := &testutil.MockOperation{}

	t.Run("VMGet_default_returns_nil_nil", func(t *testing.T) {
		result, err := m.VMGet(ctx, inputs.VMInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("VMGet_custom_func", func(t *testing.T) {
		expected := &model.VM{ID: "vm-1", Name: "test-vm"}
		m.MockVMAPI.VMGetFunc = func(ctx context.Context, input inputs.VMInput) (*model.VM, error) {
			return expected, nil
		}
		result, err := m.VMGet(ctx, inputs.VMInput{Identifiers: []string{"vm-1"}})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "vm-1", result.ID)
		assert.Equal(t, "test-vm", result.Name)
	})

	t.Run("VMGet_custom_returns_error", func(t *testing.T) {
		m := &testutil.MockOperation{}
		m.MockVMAPI.VMGetFunc = func(ctx context.Context, input inputs.VMInput) (*model.VM, error) {
			return nil, errors.New("vm not found")
		}
		_, err := m.VMGet(ctx, inputs.VMInput{Identifiers: []string{"nonexistent"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "vm not found")
		return
	})
}

func TestMockOperation_EmbeddedHostAPI(t *testing.T) {
	m := &testutil.MockOperation{}

	t.Run("HostGetState_default_returns_nil_nil", func(t *testing.T) {
		result, err := m.HostGetState(ctx)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("HostGetState_custom_func", func(t *testing.T) {
		expected := &model.HostStateItem{
			InitializedAt: "2025-01-01T00:00:00Z",
		}
		m.MockHostAPI.HostGetStateFunc = func(ctx context.Context) (*model.HostStateItem, error) {
			return expected, nil
		}
		result, err := m.HostGetState(ctx)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "2025-01-01T00:00:00Z", result.InitializedAt)
	})
}
