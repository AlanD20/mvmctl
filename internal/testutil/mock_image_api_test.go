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
	"mvmctl/pkg/errs"
)

// ─── MockImageAPI ──────────────────────────────────────────────────────────────
// Rationale: MockImageAPI implements api.ImageAPI for testing. These tests verify
// default zero-value returns and custom function routing.

func TestMockImageAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockImageAPI{}

	t.Run("ImagePull_returns_nil_nil", func(t *testing.T) {
		result, err := m.ImagePull(ctx, inputs.ImagePullInput{}, nil)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("ImageRemove_returns_nil", func(t *testing.T) {
		result := m.ImageRemove(ctx, inputs.ImageInput{}, false)
		assert.Nil(t, result)
	})

	t.Run("ImageListAll_returns_nil_nil_nil", func(t *testing.T) {
		items, versions, err := m.ImageListAll(ctx, false, "", false, nil)
		assert.NoError(t, err)
		assert.Nil(t, items)
		assert.Nil(t, versions)
	})

	t.Run("ImageGet_returns_nil_nil", func(t *testing.T) {
		result, err := m.ImageGet(ctx, inputs.ImageInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockImageAPI_CustomFunc(t *testing.T) {
	t.Run("ImagePull_custom_func", func(t *testing.T) {
		expected := &model.ImageItem{ID: "img-1", Name: "ubuntu-24.04"}
		m := &testutil.MockImageAPI{
			ImagePullFunc: func(ctx context.Context, input inputs.ImagePullInput, onProgress event.OnProgressCallback) (*model.ImageItem, error) {
				return expected, nil
			},
		}
		result, err := m.ImagePull(ctx, inputs.ImagePullInput{}, nil)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("ImagePull() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("ImageRemove_custom_func", func(t *testing.T) {
		expected := &errs.BatchResult{Items: []errs.OperationResult{
			{Status: "success", Code: "image.removed"},
		}}
		m := &testutil.MockImageAPI{
			ImageRemoveFunc: func(ctx context.Context, input inputs.ImageInput, force bool) *errs.BatchResult {
				return expected
			},
		}
		result := m.ImageRemove(ctx, inputs.ImageInput{Identifiers: []string{"ubuntu-24.04"}}, true)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("ImageRemove() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("ImageImport_custom_func", func(t *testing.T) {
		expected := &model.ImageItem{ID: "img-2", Name: "custom-image"}
		m := &testutil.MockImageAPI{
			ImageImportFunc: func(ctx context.Context, input inputs.ImageImportInput, onProgress event.OnProgressCallback) (*model.ImageItem, error) {
				return expected, nil
			},
		}
		result, err := m.ImageImport(ctx, inputs.ImageImportInput{}, nil)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("ImageImport() mismatch (-want +got):\n%s", diff)
		}
	})
}
