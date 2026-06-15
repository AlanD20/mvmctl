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

// ─── MockKernelAPI ─────────────────────────────────────────────────────────────
// Rationale: MockKernelAPI implements api.KernelAPI for testing. These tests
// verify default zero-value returns and custom function routing.

func TestMockKernelAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockKernelAPI{}

	t.Run("KernelPull_returns_nil_nil", func(t *testing.T) {
		result, err := m.KernelPull(ctx, inputs.KernelPullInput{}, nil)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("KernelRemove_returns_nil", func(t *testing.T) {
		result := m.KernelRemove(ctx, inputs.KernelInput{})
		assert.Nil(t, result)
	})

	t.Run("KernelList_returns_nil_nil_nil", func(t *testing.T) {
		items, versions, err := m.KernelList(ctx, false, false, nil)
		assert.NoError(t, err)
		assert.Nil(t, items)
		assert.Nil(t, versions)
	})

	t.Run("KernelGet_returns_nil_nil", func(t *testing.T) {
		result, err := m.KernelGet(ctx, "")
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockKernelAPI_CustomFunc(t *testing.T) {
	t.Run("KernelPull_custom_func", func(t *testing.T) {
		expected := &model.KernelItem{ID: "k-1", Version: "6.8.0"}
		m := &testutil.MockKernelAPI{
			KernelPullFunc: func(ctx context.Context, input inputs.KernelPullInput, onProgress event.OnProgressCallback) (*model.KernelItem, error) {
				return expected, nil
			},
		}
		result, err := m.KernelPull(ctx, inputs.KernelPullInput{Version: "6.8.0"}, nil)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("KernelPull() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("KernelRemove_custom_func", func(t *testing.T) {
		expected := &errs.BatchResult{Items: []errs.OperationResult{
			{Status: "success", Code: "kernel.removed"},
		}}
		m := &testutil.MockKernelAPI{
			KernelRemoveFunc: func(ctx context.Context, input inputs.KernelInput) *errs.BatchResult {
				return expected
			},
		}
		result := m.KernelRemove(ctx, inputs.KernelInput{Identifiers: []string{"vmlinux-6.8"}})
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("KernelRemove() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("KernelImport_custom_func", func(t *testing.T) {
		expected := &model.KernelItem{ID: "k-2", Version: "custom"}
		m := &testutil.MockKernelAPI{
			KernelImportFunc: func(ctx context.Context, input inputs.KernelImportInput) (*model.KernelItem, error) {
				return expected, nil
			},
		}
		result, err := m.KernelImport(ctx, inputs.KernelImportInput{})
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("KernelImport() mismatch (-want +got):\n%s", diff)
		}
	})
}
