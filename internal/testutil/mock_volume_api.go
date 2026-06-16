package testutil

import (
	"context"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// MockVolumeAPI implements api.VolumeAPI for testing.
type MockVolumeAPI struct {
	VolumeListAllFunc func(ctx context.Context) []*model.VolumeItem
	VolumeCreateFunc  func(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error)
	VolumeRemoveFunc  func(ctx context.Context, input inputs.VolumeInput, force bool) *errs.BatchResult
	VolumeInspectFunc func(ctx context.Context, input inputs.VolumeInput) (*results.VolumeInspect, error)
	VolumeResizeFunc  func(ctx context.Context, input inputs.VolumeCreateInput) error
	VolumeGetFunc     func(ctx context.Context, input inputs.VolumeInput) (*model.VolumeItem, error)
}

func (m *MockVolumeAPI) VolumeListAll(ctx context.Context) []*model.VolumeItem {
	if m.VolumeListAllFunc != nil {
		return m.VolumeListAllFunc(ctx)
	}
	return nil
}

func (m *MockVolumeAPI) VolumeCreate(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error) {
	if m.VolumeCreateFunc != nil {
		return m.VolumeCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockVolumeAPI) VolumeRemove(ctx context.Context, input inputs.VolumeInput, force bool) *errs.BatchResult {
	if m.VolumeRemoveFunc != nil {
		return m.VolumeRemoveFunc(ctx, input, force)
	}
	return nil
}

func (m *MockVolumeAPI) VolumeInspect(ctx context.Context, input inputs.VolumeInput) (*results.VolumeInspect, error) {
	if m.VolumeInspectFunc != nil {
		return m.VolumeInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockVolumeAPI) VolumeResize(ctx context.Context, input inputs.VolumeCreateInput) error {
	if m.VolumeResizeFunc != nil {
		return m.VolumeResizeFunc(ctx, input)
	}
	return nil
}

func (m *MockVolumeAPI) VolumeGet(ctx context.Context, input inputs.VolumeInput) (*model.VolumeItem, error) {
	if m.VolumeGetFunc != nil {
		return m.VolumeGetFunc(ctx, input)
	}
	return nil, nil
}
