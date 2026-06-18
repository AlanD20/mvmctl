package testutil_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// --- MockVolumeAPI ---
// Rationale: MockVolumeAPI implements api.VolumeAPI for testing. These tests
// verify default zero-value returns and custom function routing.

func TestMockVolumeAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockVolumeAPI{}

	t.Run("VolumeCreate_returns_nil_nil", func(t *testing.T) {
		result, err := m.VolumeCreate(ctx, inputs.VolumeCreateInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("VolumeRemove_returns_nil", func(t *testing.T) {
		result := m.VolumeRemove(ctx, inputs.VolumeInput{}, false)
		assert.Nil(t, result)
	})

	t.Run("VolumeListAll_returns_nil", func(t *testing.T) {
		result := m.VolumeListAll(ctx)
		assert.Nil(t, result)
	})

	t.Run("VolumeGet_returns_nil_nil", func(t *testing.T) {
		result, err := m.VolumeGet(ctx, inputs.VolumeInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockVolumeAPI_CustomFunc(t *testing.T) {
	t.Run("VolumeCreate_custom_func", func(t *testing.T) {
		expected := &model.VolumeItem{ID: "vol-1", Name: "data-vol", SizeBytes: 1073741824}
		m := &testutil.MockVolumeAPI{
			VolumeCreateFunc: func(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error) {
				return expected, nil
			},
		}
		result, err := m.VolumeCreate(ctx, inputs.VolumeCreateInput{Name: "data-vol"})
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("VolumeCreate() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("VolumeRemove_custom_func", func(t *testing.T) {
		expected := &errs.BatchResult{Items: []errs.OperationResult{
			{Status: "success", Code: "volume.removed"},
		}}
		m := &testutil.MockVolumeAPI{
			VolumeRemoveFunc: func(ctx context.Context, input inputs.VolumeInput, force bool) *errs.BatchResult {
				return expected
			},
		}
		result := m.VolumeRemove(ctx, inputs.VolumeInput{Identifiers: []string{"data-vol"}}, true)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("VolumeRemove() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("VolumeListAll_custom_func", func(t *testing.T) {
		expected := []*model.VolumeItem{
			{ID: "vol-1", Name: "alpha"},
			{ID: "vol-2", Name: "beta"},
		}
		m := &testutil.MockVolumeAPI{
			VolumeListAllFunc: func(ctx context.Context) []*model.VolumeItem {
				return expected
			},
		}
		result := m.VolumeListAll(ctx)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("VolumeListAll() mismatch (-want +got):\n%s", diff)
		}
	})
}
