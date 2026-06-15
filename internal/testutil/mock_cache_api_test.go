package testutil_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// ─── MockCacheAPI ─────────────────────────────────────────────────────────────
// Rationale: MockCacheAPI satisfies api.CacheAPI for testing upper layers. With
// 11 methods, tests verify default zero returns for each return shape and custom
// function routing for representative methods.

func TestMockCacheAPI_DefaultReturnsZero(t *testing.T) {
	m := &testutil.MockCacheAPI{}

	t.Run("CacheCheckPrivileges_returns_nil", func(t *testing.T) {
		err := m.CacheCheckPrivileges("firecracker", "read")
		assert.NoError(t, err)
	})

	t.Run("CacheSessionHasGroup_returns_false", func(t *testing.T) {
		got := m.CacheSessionHasGroup()
		assert.False(t, got)
	})

	t.Run("CacheInitAll_returns_nil_nil", func(t *testing.T) {
		result, err := m.CacheInitAll(ctx, nil)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("CachePruneVMs_returns_nil", func(t *testing.T) {
		result := m.CachePruneVMs(ctx, false, false)
		assert.Nil(t, result)
	})

	t.Run("CachePruneNetworks_returns_nil_nil", func(t *testing.T) {
		result, err := m.CachePruneNetworks(ctx, false, false)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("CachePruneImages_returns_nil_nil", func(t *testing.T) {
		result, err := m.CachePruneImages(ctx, false, false)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("CachePruneKernels_returns_nil_nil", func(t *testing.T) {
		result, err := m.CachePruneKernels(ctx, false, false)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("CachePruneBinaries_returns_nil_nil", func(t *testing.T) {
		result, err := m.CachePruneBinaries(ctx, false, false)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("CachePruneMisc_returns_nil_nil", func(t *testing.T) {
		result, err := m.CachePruneMisc(ctx, false)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("CachePruneAll_returns_nil_nil", func(t *testing.T) {
		result, err := m.CachePruneAll(ctx, false, false)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("CacheClean_returns_nil_nil", func(t *testing.T) {
		result, err := m.CacheClean(ctx, false)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockCacheAPI_CustomFunc(t *testing.T) {
	t.Run("CacheInitAll_custom_func", func(t *testing.T) {
		expected := &results.CacheInitResult{
			CacheDir: "/tmp/cache",
			Directories: []string{"kernels", "images"},
		}
		m := &testutil.MockCacheAPI{
			CacheInitAllFunc: func(ctx context.Context, onProgress event.OnProgressCallback) (*results.CacheInitResult, error) {
				return expected, nil
			},
		}
		result, err := m.CacheInitAll(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("CacheInitAll() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("CachePruneAll_custom_func", func(t *testing.T) {
		expected := &model.PruneAllResult{
			PrunedIDs: []string{"vm-1", "vm-2"},
			FailedIDs: nil,
		}
		m := &testutil.MockCacheAPI{
			CachePruneAllFunc: func(ctx context.Context, dryRun bool, includeAll bool) (*model.PruneAllResult, error) {
				return expected, nil
			},
		}
		result, err := m.CachePruneAll(ctx, false, true)
		require.NoError(t, err)
		require.NotNil(t, result)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("CachePruneAll() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("CachePruneVMs_custom_func", func(t *testing.T) {
		m := &testutil.MockCacheAPI{
			CachePruneVMsFunc: func(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
				return &errs.OperationResult{}
			},
		}
		result := m.CachePruneVMs(ctx, true, false)
		require.NotNil(t, result)
	})

	t.Run("CacheSessionHasGroup_custom_func", func(t *testing.T) {
		m := &testutil.MockCacheAPI{
			CacheSessionHasGroupFunc: func() bool {
				return true
			},
		}
		got := m.CacheSessionHasGroup()
		assert.True(t, got)
	})

	t.Run("CacheCheckPrivileges_custom_func_returns_error", func(t *testing.T) {
		m := &testutil.MockCacheAPI{
			CacheCheckPrivilegesFunc: func(binary, operation string) error {
				return errors.New("permission denied")
			},
		}
		err := m.CacheCheckPrivileges("firecracker", "read")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permission denied")
		return
	})
}
