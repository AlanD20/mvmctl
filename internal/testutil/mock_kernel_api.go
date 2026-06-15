package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// MockKernelAPI implements api.KernelAPI for testing.
type MockKernelAPI struct {
	KernelPruneFunc     func(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	KernelPullFunc      func(ctx context.Context, input inputs.KernelPullInput, onProgress event.OnProgressCallback) (*model.KernelItem, error)
	KernelImportFunc    func(ctx context.Context, input inputs.KernelImportInput) (*model.KernelItem, error)
	KernelRemoveFunc    func(ctx context.Context, input inputs.KernelInput) *errs.BatchResult
	KernelListFunc      func(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error)
	KernelGetFunc       func(ctx context.Context, identifier string) (*model.KernelItem, error)
	KernelInspectFunc   func(ctx context.Context, identifier string) (*results.KernelInspect, error)
	KernelSetDefaultFunc func(ctx context.Context, identifier string) error
}

func (m *MockKernelAPI) KernelPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if m.KernelPruneFunc != nil {
		return m.KernelPruneFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockKernelAPI) KernelPull(ctx context.Context, input inputs.KernelPullInput, onProgress event.OnProgressCallback) (*model.KernelItem, error) {
	if m.KernelPullFunc != nil {
		return m.KernelPullFunc(ctx, input, onProgress)
	}
	return nil, nil
}

func (m *MockKernelAPI) KernelImport(ctx context.Context, input inputs.KernelImportInput) (*model.KernelItem, error) {
	if m.KernelImportFunc != nil {
		return m.KernelImportFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockKernelAPI) KernelRemove(ctx context.Context, input inputs.KernelInput) *errs.BatchResult {
	if m.KernelRemoveFunc != nil {
		return m.KernelRemoveFunc(ctx, input)
	}
	return nil
}

func (m *MockKernelAPI) KernelList(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error) {
	if m.KernelListFunc != nil {
		return m.KernelListFunc(ctx, remote, noCache, onProgress)
	}
	return nil, nil, nil
}

func (m *MockKernelAPI) KernelGet(ctx context.Context, identifier string) (*model.KernelItem, error) {
	if m.KernelGetFunc != nil {
		return m.KernelGetFunc(ctx, identifier)
	}
	return nil, nil
}

func (m *MockKernelAPI) KernelInspect(ctx context.Context, identifier string) (*results.KernelInspect, error) {
	if m.KernelInspectFunc != nil {
		return m.KernelInspectFunc(ctx, identifier)
	}
	return nil, nil
}

func (m *MockKernelAPI) KernelSetDefault(ctx context.Context, identifier string) error {
	if m.KernelSetDefaultFunc != nil {
		return m.KernelSetDefaultFunc(ctx, identifier)
	}
	return nil
	}
