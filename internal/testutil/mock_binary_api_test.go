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
	"mvmctl/pkg/api/inputs"
)

// ─── MockBinaryAPI ─────────────────────────────────────────────────────────────
// Rationale: MockBinaryAPI implements api.BinaryAPI for testing. These tests
// verify default zero-value returns and custom function routing.

func TestMockBinaryAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockBinaryAPI{}

	t.Run("BinaryPull_returns_nil_nil", func(t *testing.T) {
		result, err := m.BinaryPull(ctx, inputs.BinaryPullInput{}, nil)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("BinaryRemove_returns_nil", func(t *testing.T) {
		result := m.BinaryRemove(ctx, inputs.BinaryInput{}, false)
		assert.Nil(t, result)
	})

	t.Run("BinaryList_returns_nil_nil_nil", func(t *testing.T) {
		items, versions, err := m.BinaryList(ctx, false, nil, nil)
		assert.NoError(t, err)
		assert.Nil(t, items)
		assert.Nil(t, versions)
	})

	t.Run("BinaryGet_returns_nil_nil", func(t *testing.T) {
		result, err := m.BinaryGet(ctx, inputs.BinaryInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockBinaryAPI_CustomFunc(t *testing.T) {
	t.Run("BinaryPull_custom_func", func(t *testing.T) {
		expected := []*model.BinaryItem{
			{ID: "bin-1", Version: "v1.0.0"},
		}
		m := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(ctx context.Context, input inputs.BinaryPullInput, onProgress event.OnProgressCallback) ([]*model.BinaryItem, error) {
				return expected, nil
			},
		}
		result, err := m.BinaryPull(ctx, inputs.BinaryPullInput{Version: "v1.0.0"}, nil)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("BinaryPull() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("BinaryList_custom_func", func(t *testing.T) {
		expected := []*model.BinaryItem{
			{ID: "bin-1", Version: "v1.0.0"},
			{ID: "bin-2", Version: "v2.0.0"},
		}
		expectedVersions := []model.VersionInfo{
			{Version: "v1.0.0", IsPresent: true},
		}
		m := &testutil.MockBinaryAPI{
			BinaryListFunc: func(ctx context.Context, remote bool, limit *int, onProgress event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				return expected, expectedVersions, nil
			},
		}
		result, versions, err := m.BinaryList(ctx, false, nil, nil)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("BinaryList() items mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(expectedVersions, versions); diff != "" {
			t.Errorf("BinaryList() versions mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("BinarySetDefault_custom_func", func(t *testing.T) {
		expected := &model.BinaryItem{ID: "bin-1", Version: "v1.0.0"}
		m := &testutil.MockBinaryAPI{
			BinarySetDefaultFunc: func(ctx context.Context, input inputs.BinaryInput) (*model.BinaryItem, error) {
				return expected, nil
			},
		}
		result, err := m.BinarySetDefault(ctx, inputs.BinaryInput{Identifiers: []string{"firecracker"}})
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("BinarySetDefault() mismatch (-want +got):\n%s", diff)
		}
	})
}
