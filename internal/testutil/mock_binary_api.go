package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// MockBinaryAPI implements api.BinaryAPI for testing.
type MockBinaryAPI struct {
	BinaryPruneFunc           func(ctx context.Context, dryRun bool, force bool) ([]string, error)
	BinaryPullFunc            func(ctx context.Context, input inputs.BinaryPullInput, onProgress event.OnProgressCallback) ([]*model.BinaryItem, error)
	BinaryRemoveFunc          func(ctx context.Context, input inputs.BinaryInput, force bool) *errs.BatchResult
	BinaryRemoveByVersionFunc func(ctx context.Context, version string, force bool) error
	BinaryListFunc            func(ctx context.Context, remote bool, limit *int, onProgress event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error)
	BinaryGetFunc             func(ctx context.Context, input inputs.BinaryInput) ([]*model.BinaryItem, error)
	BinarySetDefaultFunc      func(ctx context.Context, input inputs.BinaryInput) (*model.BinaryItem, error)
	BinaryEnsureDefaultFunc   func(ctx context.Context) (*model.BinaryItem, error)
}

func (m *MockBinaryAPI) BinaryPrune(ctx context.Context, dryRun bool, force bool) ([]string, error) {
	if m.BinaryPruneFunc != nil {
		return m.BinaryPruneFunc(ctx, dryRun, force)
	}
	return nil, nil
}

func (m *MockBinaryAPI) BinaryPull(
	ctx context.Context,
	input inputs.BinaryPullInput,
	onProgress event.OnProgressCallback,
) ([]*model.BinaryItem, error) {
	if m.BinaryPullFunc != nil {
		return m.BinaryPullFunc(ctx, input, onProgress)
	}
	return nil, nil
}

func (m *MockBinaryAPI) BinaryRemove(ctx context.Context, input inputs.BinaryInput, force bool) *errs.BatchResult {
	if m.BinaryRemoveFunc != nil {
		return m.BinaryRemoveFunc(ctx, input, force)
	}
	return nil
}

func (m *MockBinaryAPI) BinaryRemoveByVersion(ctx context.Context, version string, force bool) error {
	if m.BinaryRemoveByVersionFunc != nil {
		return m.BinaryRemoveByVersionFunc(ctx, version, force)
	}
	return nil
}

func (m *MockBinaryAPI) BinaryList(
	ctx context.Context,
	remote bool,
	limit *int,
	onProgress event.OnProgressCallback,
) ([]*model.BinaryItem, []model.VersionInfo, error) {
	if m.BinaryListFunc != nil {
		return m.BinaryListFunc(ctx, remote, limit, onProgress)
	}
	return nil, nil, nil
}

func (m *MockBinaryAPI) BinaryGet(ctx context.Context, input inputs.BinaryInput) ([]*model.BinaryItem, error) {
	if m.BinaryGetFunc != nil {
		return m.BinaryGetFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockBinaryAPI) BinarySetDefault(ctx context.Context, input inputs.BinaryInput) (*model.BinaryItem, error) {
	if m.BinarySetDefaultFunc != nil {
		return m.BinarySetDefaultFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockBinaryAPI) BinaryEnsureDefault(ctx context.Context) (*model.BinaryItem, error) {
	if m.BinaryEnsureDefaultFunc != nil {
		return m.BinaryEnsureDefaultFunc(ctx)
	}
	return nil, nil
}
