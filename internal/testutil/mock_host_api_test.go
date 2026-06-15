package testutil_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/results"
)

// ─── MockHostAPI ───────────────────────────────────────────────────────────────
// Rationale: MockHostAPI implements api.HostAPI for testing. These tests verify
// default zero-value returns and custom function routing.

func TestMockHostAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockHostAPI{}

	t.Run("HostInit_returns_nil_nil", func(t *testing.T) {
		result, err := m.HostInit(ctx, nil)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("HostInfo_returns_nil_nil", func(t *testing.T) {
		result, err := m.HostInfo(ctx)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("HostGetState_returns_nil_nil", func(t *testing.T) {
		result, err := m.HostGetState(ctx)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("HostCheckKVMAccess_returns_false", func(t *testing.T) {
		result := m.HostCheckKVMAccess()
		assert.False(t, result)
	})

	t.Run("HostIsInitialized_returns_false", func(t *testing.T) {
		result := m.HostIsInitialized(ctx)
		assert.False(t, result)
	})

	t.Run("HostGetIPForwardStatus_returns_empty", func(t *testing.T) {
		result, err := m.HostGetIPForwardStatus(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "", result)
	})
}

func TestMockHostAPI_CustomFunc(t *testing.T) {
	t.Run("HostInit_custom_func", func(t *testing.T) {
		expected := map[string]string{"status": "ok"}
		m := &testutil.MockHostAPI{
			HostInitFunc: func(ctx context.Context, onProgress event.OnProgressCallback) (any, error) {
				return expected, nil
			},
		}
		result, err := m.HostInit(ctx, nil)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("HostInit() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("HostInfo_custom_func", func(t *testing.T) {
		expected := &results.HostInfo{
			Hostname: "test-host",
		}
		m := &testutil.MockHostAPI{
			HostInfoFunc: func(ctx context.Context) (*results.HostInfo, error) {
				return expected, nil
			},
		}
		result, err := m.HostInfo(ctx)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("HostInfo() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("HostCheckKVMAccess_custom_func", func(t *testing.T) {
		m := &testutil.MockHostAPI{
			HostCheckKVMAccessFunc: func() bool {
				return true
			},
		}
		result := m.HostCheckKVMAccess()
		assert.True(t, result)
	})

	t.Run("HostIsInitialized_custom_func", func(t *testing.T) {
		m := &testutil.MockHostAPI{
			HostIsInitializedFunc: func(ctx context.Context) bool {
				return true
			},
		}
		result := m.HostIsInitialized(ctx)
		assert.True(t, result)
	})

	t.Run("HostGetRunningVMs_custom_func", func(t *testing.T) {
		expected := []*model.VM{{ID: "vm-1", Name: "running-vm"}}
		m := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(ctx context.Context) ([]*model.VM, error) {
				return expected, nil
			},
		}
		result, err := m.HostGetRunningVMs(ctx)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("HostGetRunningVMs() mismatch (-want +got):\n%s", diff)
		}
	})
}
