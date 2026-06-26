package testutil_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

// --- MockConfigAPI ---
// Rationale: MockConfigAPI satisfies api.ConfigAPI for testing upper layers.
// Tests verify default zero returns and custom function routing for ConfigGet
// and ConfigSet.

func TestMockConfigAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockConfigAPI{}

	t.Run("ConfigGet_returns_nil_nil", func(t *testing.T) {
		result, err := m.ConfigGet(ctx, "test", "key")
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("ConfigSet_returns_nil", func(t *testing.T) {
		err := m.ConfigSet(ctx, "test", "key", "val")
		assert.NoError(t, err)
	})

	t.Run("ConfigReset_returns_zero_nil", func(t *testing.T) {
		count, err := m.ConfigReset(ctx, "test", "key", false)
		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("ConfigListAll_returns_nil_nil", func(t *testing.T) {
		result, err := m.ConfigListAll(ctx)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockConfigAPI_CustomFunc(t *testing.T) {
	t.Run("ConfigGet_custom_func", func(t *testing.T) {
		expected := "test-value"
		m := &testutil.MockConfigAPI{
			ConfigGetFunc: func(ctx context.Context, category, key string) (any, error) {
				return expected, nil
			},
		}
		result, err := m.ConfigGet(ctx, "test", "key")
		require.NoError(t, err)
		assert.Equal(t, expected, result)
	})

	t.Run("ConfigSet_custom_func_returns_error", func(t *testing.T) {
		m := &testutil.MockConfigAPI{
			ConfigSetFunc: func(ctx context.Context, category, key string, value any) error {
				return errors.New("config set failed")
			},
		}
		err := m.ConfigSet(ctx, "test", "key", "val")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "config set failed")
		return
	})

	t.Run("ConfigReset_custom_func", func(t *testing.T) {
		m := &testutil.MockConfigAPI{
			ConfigResetFunc: func(ctx context.Context, category, key string, allOverrides bool) (int, error) {
				return 3, nil
			},
		}
		count, err := m.ConfigReset(ctx, "test", "key", true)
		require.NoError(t, err)
		assert.Equal(t, 3, count)
	})

	t.Run("ConfigListAll_custom_func", func(t *testing.T) {
		expected := map[string]map[string]model.SettingInfo{
			"general": {
				"debug": {Type: "bool", Default: false, Override: nil},
			},
		}
		m := &testutil.MockConfigAPI{
			ConfigListAllFunc: func(ctx context.Context) (map[string]map[string]model.SettingInfo, error) {
				return expected, nil
			},
		}
		result, err := m.ConfigListAll(ctx)
		require.NoError(t, err)
		require.NotNil(t, result)
		if diff := cmp.Diff(expected["general"], result["general"]); diff != "" {
			t.Errorf("ConfigListAll() mismatch (-want +got):\n%s", diff)
		}
	})
}
