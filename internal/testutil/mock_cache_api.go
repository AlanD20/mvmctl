package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// MockCacheAPI implements api.CacheAPI for testing.
type MockCacheAPI struct {
	CacheCheckPrivilegesFunc func(binary string, operation string) error
	CacheSessionHasGroupFunc func() bool
	CacheInitAllFunc         func(ctx context.Context, onProgress event.OnProgressCallback) (*results.CacheInitResult, error)
	CachePruneVMsFunc        func(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult
	CachePruneNetworksFunc   func(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	CachePruneImagesFunc     func(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	CachePruneKernelsFunc    func(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	CachePruneBinariesFunc   func(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	CachePruneMiscFunc       func(ctx context.Context, dryRun bool) (map[string]any, error)
	CachePruneAllFunc        func(ctx context.Context, dryRun bool, includeAll bool) (*model.PruneAllResult, error)
	CacheCleanFunc           func(ctx context.Context, dryRun bool) (*model.CleanResult, error)
}

func (m *MockCacheAPI) CacheCheckPrivileges(binary string, operation string) error {
	if m.CacheCheckPrivilegesFunc != nil {
		return m.CacheCheckPrivilegesFunc(binary, operation)
	}
	return nil
}

func (m *MockCacheAPI) CacheSessionHasGroup() bool {
	if m.CacheSessionHasGroupFunc != nil {
		return m.CacheSessionHasGroupFunc()
	}
	return false
}

func (m *MockCacheAPI) CacheInitAll(
	ctx context.Context,
	onProgress event.OnProgressCallback,
) (*results.CacheInitResult, error) {
	if m.CacheInitAllFunc != nil {
		return m.CacheInitAllFunc(ctx, onProgress)
	}
	return nil, nil
}

func (m *MockCacheAPI) CachePruneVMs(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	if m.CachePruneVMsFunc != nil {
		return m.CachePruneVMsFunc(ctx, dryRun, includeAll)
	}
	return nil
}

func (m *MockCacheAPI) CachePruneNetworks(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if m.CachePruneNetworksFunc != nil {
		return m.CachePruneNetworksFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockCacheAPI) CachePruneImages(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if m.CachePruneImagesFunc != nil {
		return m.CachePruneImagesFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockCacheAPI) CachePruneKernels(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if m.CachePruneKernelsFunc != nil {
		return m.CachePruneKernelsFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockCacheAPI) CachePruneBinaries(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if m.CachePruneBinariesFunc != nil {
		return m.CachePruneBinariesFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockCacheAPI) CachePruneMisc(ctx context.Context, dryRun bool) (map[string]any, error) {
	if m.CachePruneMiscFunc != nil {
		return m.CachePruneMiscFunc(ctx, dryRun)
	}
	return nil, nil
}

func (m *MockCacheAPI) CachePruneAll(ctx context.Context, dryRun bool, includeAll bool) (*model.PruneAllResult, error) {
	if m.CachePruneAllFunc != nil {
		return m.CachePruneAllFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockCacheAPI) CacheClean(ctx context.Context, dryRun bool) (*model.CleanResult, error) {
	if m.CacheCleanFunc != nil {
		return m.CacheCleanFunc(ctx, dryRun)
	}
	return nil, nil
}
