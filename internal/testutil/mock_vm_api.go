package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// MockVMAPI implements api.VMAPI for testing.
type MockVMAPI struct {
	VMCreateFunc       func(ctx context.Context, input inputs.VMCreateInput, onProgress event.OnProgressCallback) ([]*model.VMItem, error)
	VMRemoveFunc       func(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMPruneFunc        func(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	VMListFunc         func(ctx context.Context, statuses ...string) []*model.VMItem
	VMGetFunc          func(ctx context.Context, input inputs.VMInput) (*model.VMItem, error)
	VMInspectFunc      func(ctx context.Context, input inputs.VMInput) (*results.VMInspect, error)
	VMStartFunc        func(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMStopFunc         func(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMRebootFunc       func(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMPauseFunc        func(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMResumeFunc       func(ctx context.Context, input inputs.VMInput) *errs.BatchResult
	VMSnapshotFunc     func(ctx context.Context, input inputs.VMInput, memFile string, stateFile string) error
	VMLoadFunc         func(ctx context.Context, input inputs.VMInput, memFile string, stateFile string, resume bool, rootfs string) error
	VMAttachVolumeFunc func(ctx context.Context, input inputs.VMInput, volumeName string) error
	VMDetachVolumeFunc func(ctx context.Context, input inputs.VMInput, volumeName string) error
	VMExecFunc         func(ctx context.Context, input inputs.VMExecInput) (*results.VMExecResult, error)
}

func (m *MockVMAPI) VMCreate(
	ctx context.Context,
	input inputs.VMCreateInput,
	onProgress event.OnProgressCallback,
) ([]*model.VMItem, error) {
	if m.VMCreateFunc != nil {
		return m.VMCreateFunc(ctx, input, onProgress)
	}
	return nil, nil
}

func (m *MockVMAPI) VMRemove(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	if m.VMRemoveFunc != nil {
		return m.VMRemoveFunc(ctx, input)
	}
	return nil
}

func (m *MockVMAPI) VMPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if m.VMPruneFunc != nil {
		return m.VMPruneFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockVMAPI) VMList(ctx context.Context, statuses ...string) []*model.VMItem {
	if m.VMListFunc != nil {
		return m.VMListFunc(ctx, statuses...)
	}
	return nil
}

func (m *MockVMAPI) VMGet(ctx context.Context, input inputs.VMInput) (*model.VMItem, error) {
	if m.VMGetFunc != nil {
		return m.VMGetFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockVMAPI) VMInspect(ctx context.Context, input inputs.VMInput) (*results.VMInspect, error) {
	if m.VMInspectFunc != nil {
		return m.VMInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockVMAPI) VMStart(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	if m.VMStartFunc != nil {
		return m.VMStartFunc(ctx, input)
	}
	return nil
}

func (m *MockVMAPI) VMStop(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	if m.VMStopFunc != nil {
		return m.VMStopFunc(ctx, input)
	}
	return nil
}

func (m *MockVMAPI) VMReboot(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	if m.VMRebootFunc != nil {
		return m.VMRebootFunc(ctx, input)
	}
	return nil
}

func (m *MockVMAPI) VMPause(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	if m.VMPauseFunc != nil {
		return m.VMPauseFunc(ctx, input)
	}
	return nil
}

func (m *MockVMAPI) VMResume(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
	if m.VMResumeFunc != nil {
		return m.VMResumeFunc(ctx, input)
	}
	return nil
}

func (m *MockVMAPI) VMSnapshot(ctx context.Context, input inputs.VMInput, memFile string, stateFile string) error {
	if m.VMSnapshotFunc != nil {
		return m.VMSnapshotFunc(ctx, input, memFile, stateFile)
	}
	return nil
}

func (m *MockVMAPI) VMLoad(
	ctx context.Context,
	input inputs.VMInput,
	memFile string,
	stateFile string,
	resume bool,
	rootfs string,
) error {
	if m.VMLoadFunc != nil {
		return m.VMLoadFunc(ctx, input, memFile, stateFile, resume, rootfs)
	}
	return nil
}

func (m *MockVMAPI) VMAttachVolume(ctx context.Context, input inputs.VMInput, volumeName string) error {
	if m.VMAttachVolumeFunc != nil {
		return m.VMAttachVolumeFunc(ctx, input, volumeName)
	}
	return nil
}

func (m *MockVMAPI) VMDetachVolume(ctx context.Context, input inputs.VMInput, volumeName string) error {
	if m.VMDetachVolumeFunc != nil {
		return m.VMDetachVolumeFunc(ctx, input, volumeName)
	}
	return nil
}

func (m *MockVMAPI) VMExec(ctx context.Context, input inputs.VMExecInput) (*results.VMExecResult, error) {
	if m.VMExecFunc != nil {
		return m.VMExecFunc(ctx, input)
	}
	return nil, nil
}
