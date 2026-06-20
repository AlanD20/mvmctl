package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// MockSnapshotAPI implements api.SnapshotAPI for testing.
type MockSnapshotAPI struct {
	SnapshotCreateFunc  func(ctx context.Context, input inputs.SnapshotCreateInput, onProgress event.OnProgressCallback) (*model.SnapshotItem, error)
	SnapshotListFunc    func(ctx context.Context) []*model.SnapshotItem
	SnapshotInspectFunc func(ctx context.Context, input inputs.SnapshotInput) (*results.SnapshotInspect, error)
	SnapshotRestoreFunc func(ctx context.Context, input inputs.SnapshotRestoreInput) ([]*model.VMItem, error)
	SnapshotRemoveFunc  func(ctx context.Context, input inputs.SnapshotInput) *errs.BatchResult
}

func (m *MockSnapshotAPI) SnapshotCreate(
	ctx context.Context,
	input inputs.SnapshotCreateInput,
	onProgress event.OnProgressCallback,
) (*model.SnapshotItem, error) {
	if m.SnapshotCreateFunc != nil {
		return m.SnapshotCreateFunc(ctx, input, onProgress)
	}
	return nil, nil
}

func (m *MockSnapshotAPI) SnapshotList(ctx context.Context) []*model.SnapshotItem {
	if m.SnapshotListFunc != nil {
		return m.SnapshotListFunc(ctx)
	}
	return nil
}

func (m *MockSnapshotAPI) SnapshotInspect(
	ctx context.Context,
	input inputs.SnapshotInput,
) (*results.SnapshotInspect, error) {
	if m.SnapshotInspectFunc != nil {
		return m.SnapshotInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockSnapshotAPI) SnapshotRestore(
	ctx context.Context,
	input inputs.SnapshotRestoreInput,
) ([]*model.VMItem, error) {
	if m.SnapshotRestoreFunc != nil {
		return m.SnapshotRestoreFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockSnapshotAPI) SnapshotRemove(ctx context.Context, input inputs.SnapshotInput) *errs.BatchResult {
	if m.SnapshotRemoveFunc != nil {
		return m.SnapshotRemoveFunc(ctx, input)
	}
	return &errs.BatchResult{}
}
