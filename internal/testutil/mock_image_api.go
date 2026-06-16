package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// MockImageAPI implements api.ImageAPI for testing.
type MockImageAPI struct {
	ImagePruneFunc      func(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	ImagePullFunc       func(ctx context.Context, input inputs.ImagePullInput, onProgress event.OnProgressCallback) (*model.ImageItem, error)
	ImageImportFunc     func(ctx context.Context, input inputs.ImageImportInput, onProgress event.OnProgressCallback) (*model.ImageItem, error)
	ImageWarmFunc       func(ctx context.Context, input inputs.ImageInput, all bool, onProgress event.OnProgressCallback) ([]string, error)
	ImageRemoveFunc     func(ctx context.Context, input inputs.ImageInput, force bool) *errs.BatchResult
	ImageListAllFunc    func(ctx context.Context, remote bool, typeFilter string, noCache bool, onProgress event.OnProgressCallback) ([]*model.ImageItem, []model.VersionInfo, error)
	ImageGetFunc        func(ctx context.Context, input inputs.ImageInput) (*model.ImageItem, error)
	ImageInspectFunc    func(ctx context.Context, input inputs.ImageInput) (*results.ImageInspect, error)
	ImageSetDefaultFunc func(ctx context.Context, input inputs.ImageInput) error
}

func (m *MockImageAPI) ImagePrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if m.ImagePruneFunc != nil {
		return m.ImagePruneFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockImageAPI) ImagePull(
	ctx context.Context,
	input inputs.ImagePullInput,
	onProgress event.OnProgressCallback,
) (*model.ImageItem, error) {
	if m.ImagePullFunc != nil {
		return m.ImagePullFunc(ctx, input, onProgress)
	}
	return nil, nil
}

func (m *MockImageAPI) ImageImport(
	ctx context.Context,
	input inputs.ImageImportInput,
	onProgress event.OnProgressCallback,
) (*model.ImageItem, error) {
	if m.ImageImportFunc != nil {
		return m.ImageImportFunc(ctx, input, onProgress)
	}
	return nil, nil
}

func (m *MockImageAPI) ImageWarm(
	ctx context.Context,
	input inputs.ImageInput,
	all bool,
	onProgress event.OnProgressCallback,
) ([]string, error) {
	if m.ImageWarmFunc != nil {
		return m.ImageWarmFunc(ctx, input, all, onProgress)
	}
	return nil, nil
}

func (m *MockImageAPI) ImageRemove(ctx context.Context, input inputs.ImageInput, force bool) *errs.BatchResult {
	if m.ImageRemoveFunc != nil {
		return m.ImageRemoveFunc(ctx, input, force)
	}
	return nil
}

func (m *MockImageAPI) ImageListAll(
	ctx context.Context,
	remote bool,
	typeFilter string,
	noCache bool,
	onProgress event.OnProgressCallback,
) ([]*model.ImageItem, []model.VersionInfo, error) {
	if m.ImageListAllFunc != nil {
		return m.ImageListAllFunc(ctx, remote, typeFilter, noCache, onProgress)
	}
	return nil, nil, nil
}

func (m *MockImageAPI) ImageGet(ctx context.Context, input inputs.ImageInput) (*model.ImageItem, error) {
	if m.ImageGetFunc != nil {
		return m.ImageGetFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockImageAPI) ImageInspect(ctx context.Context, input inputs.ImageInput) (*results.ImageInspect, error) {
	if m.ImageInspectFunc != nil {
		return m.ImageInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockImageAPI) ImageSetDefault(ctx context.Context, input inputs.ImageInput) error {
	if m.ImageSetDefaultFunc != nil {
		return m.ImageSetDefaultFunc(ctx, input)
	}
	return nil
}
