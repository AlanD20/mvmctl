package testutil

import (
	"context"

	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// MockOperation implements api.API for testing by embedding all per-domain mocks.
type MockOperation struct {
	MockVMAPI
	MockImageAPI
	MockNetworkAPI
	MockVolumeAPI
	MockKernelAPI
	MockKeyAPI
	MockBinaryAPI
	MockHostAPI
	MockConsoleAPI
	MockExecAPI
	MockReadyAPI
	MockEnvironmentFloatingIPAPI
	MockEnvironmentPolicyAPI
	MockSSHAPI
	MockConfigAPI
	MockCacheAPI
	MockLogAPI
	MockCPAPI
	MockInitAPI
	MockSnapshotAPI
	MockUpdateAPI
	MockPolicyAPI
}

// MockPolicyAPI implements api.PolicyAPI for composite-interface compatibility.
type MockPolicyAPI struct {
	LabelTrafficPolicyCreateFunc func(context.Context, inputs.LabelTrafficPolicyCreateInput) (results.LabelTrafficPolicy, error)
	LabelTrafficPolicyListFunc   func(context.Context) ([]results.LabelTrafficPolicy, error)
	LabelTrafficPolicyRemoveFunc func(context.Context, inputs.LabelTrafficPolicyInput) error

	VMToFloatingIPTrafficPolicyCreateFunc func(
		ctx context.Context,
		input inputs.VMToFloatingIPTrafficPolicyCreateInput,
	) (*results.VMToFloatingIPTrafficPolicy, error)
	VMToFloatingIPTrafficPolicyListFunc    func(ctx context.Context) ([]*results.VMToFloatingIPTrafficPolicy, error)
	VMToFloatingIPTrafficPolicyInspectFunc func(
		ctx context.Context,
		input inputs.VMToFloatingIPTrafficPolicyInput,
	) (*results.VMToFloatingIPTrafficPolicy, error)
	VMToFloatingIPTrafficPolicyRemoveFunc func(
		ctx context.Context,
		input inputs.VMToFloatingIPTrafficPolicyInput,
	) error
	NetworkToFloatingIPTrafficPolicyCreateFunc func(
		ctx context.Context,
		input inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
	) (*results.NetworkToFloatingIPTrafficPolicy, error)
	NetworkToFloatingIPTrafficPolicyListFunc func(
		ctx context.Context,
	) ([]*results.NetworkToFloatingIPTrafficPolicy, error)
	NetworkToFloatingIPTrafficPolicyInspectFunc func(
		ctx context.Context,
		input inputs.NetworkToFloatingIPTrafficPolicyInput,
	) (*results.NetworkToFloatingIPTrafficPolicy, error)
	NetworkToFloatingIPTrafficPolicyRemoveFunc func(
		ctx context.Context,
		input inputs.NetworkToFloatingIPTrafficPolicyInput,
	) error

	VMToHostTrafficPolicyCreateFunc       func(context.Context, inputs.VMToHostTrafficPolicyCreateInput) (*results.VMToHostTrafficPolicy, error)
	VMToHostTrafficPolicyListFunc         func(context.Context) ([]*results.VMToHostTrafficPolicy, error)
	VMToHostTrafficPolicyInspectFunc      func(context.Context, inputs.VMToHostTrafficPolicyInput) (*results.VMToHostTrafficPolicy, error)
	VMToHostTrafficPolicyRemoveFunc       func(context.Context, inputs.VMToHostTrafficPolicyInput) error
	NetworkToHostTrafficPolicyCreateFunc  func(context.Context, inputs.NetworkToHostTrafficPolicyCreateInput) (*results.NetworkToHostTrafficPolicy, error)
	NetworkToHostTrafficPolicyListFunc    func(context.Context) ([]*results.NetworkToHostTrafficPolicy, error)
	NetworkToHostTrafficPolicyInspectFunc func(context.Context, inputs.NetworkToHostTrafficPolicyInput) (*results.NetworkToHostTrafficPolicy, error)
	NetworkToHostTrafficPolicyRemoveFunc  func(context.Context, inputs.NetworkToHostTrafficPolicyInput) error
	NetworkToVMTrafficPolicyCreateFunc    func(context.Context, inputs.NetworkToVMTrafficPolicyCreateInput) (*results.NetworkToVMTrafficPolicy, error)
	NetworkToVMTrafficPolicyListFunc      func(context.Context) ([]*results.NetworkToVMTrafficPolicy, error)
	NetworkToVMTrafficPolicyInspectFunc   func(context.Context, inputs.NetworkToVMTrafficPolicyInput) (*results.NetworkToVMTrafficPolicy, error)
	NetworkToVMTrafficPolicyRemoveFunc    func(context.Context, inputs.NetworkToVMTrafficPolicyInput) error
	VMToVMTrafficPolicyCreateFunc         func(context.Context, inputs.VMToVMTrafficPolicyCreateInput) (*results.VMToVMTrafficPolicy, error)
	VMToVMTrafficPolicyListFunc           func(context.Context) ([]*results.VMToVMTrafficPolicy, error)
	VMToVMTrafficPolicyInspectFunc        func(context.Context, inputs.VMToVMTrafficPolicyInput) (*results.VMToVMTrafficPolicy, error)
	VMToVMTrafficPolicyRemoveFunc         func(context.Context, inputs.VMToVMTrafficPolicyInput) error
	ExecPolicyCreateFunc                  func(
		context.Context,
		inputs.VMToVMExecPolicyCreateInput,
	) (*results.VMToVMExecPolicy, error)
	ExecPolicyListFunc   func(context.Context) ([]*results.VMToVMExecPolicy, error)
	ExecPolicyRemoveFunc func(context.Context, inputs.VMToVMExecPolicyInput) error
}

func (m *MockPolicyAPI) NetworkToVMTrafficPolicyCreate(
	ctx context.Context, input inputs.NetworkToVMTrafficPolicyCreateInput,
) (*results.NetworkToVMTrafficPolicy, error) {
	if m.NetworkToVMTrafficPolicyCreateFunc != nil {
		return m.NetworkToVMTrafficPolicyCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) NetworkToVMTrafficPolicyList(ctx context.Context) ([]*results.NetworkToVMTrafficPolicy, error) {
	if m.NetworkToVMTrafficPolicyListFunc != nil {
		return m.NetworkToVMTrafficPolicyListFunc(ctx)
	}
	return nil, nil
}

func (m *MockPolicyAPI) NetworkToVMTrafficPolicyInspect(
	ctx context.Context, input inputs.NetworkToVMTrafficPolicyInput,
) (*results.NetworkToVMTrafficPolicy, error) {
	if m.NetworkToVMTrafficPolicyInspectFunc != nil {
		return m.NetworkToVMTrafficPolicyInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) NetworkToVMTrafficPolicyRemove(
	ctx context.Context, input inputs.NetworkToVMTrafficPolicyInput,
) error {
	if m.NetworkToVMTrafficPolicyRemoveFunc != nil {
		return m.NetworkToVMTrafficPolicyRemoveFunc(ctx, input)
	}
	return nil
}

func (m *MockPolicyAPI) VMToVMTrafficPolicyCreate(
	ctx context.Context, input inputs.VMToVMTrafficPolicyCreateInput,
) (*results.VMToVMTrafficPolicy, error) {
	if m.VMToVMTrafficPolicyCreateFunc != nil {
		return m.VMToVMTrafficPolicyCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) VMToVMTrafficPolicyList(ctx context.Context) ([]*results.VMToVMTrafficPolicy, error) {
	if m.VMToVMTrafficPolicyListFunc != nil {
		return m.VMToVMTrafficPolicyListFunc(ctx)
	}
	return nil, nil
}

func (m *MockPolicyAPI) VMToVMTrafficPolicyInspect(
	ctx context.Context, input inputs.VMToVMTrafficPolicyInput,
) (*results.VMToVMTrafficPolicy, error) {
	if m.VMToVMTrafficPolicyInspectFunc != nil {
		return m.VMToVMTrafficPolicyInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) VMToVMTrafficPolicyRemove(
	ctx context.Context, input inputs.VMToVMTrafficPolicyInput,
) error {
	if m.VMToVMTrafficPolicyRemoveFunc != nil {
		return m.VMToVMTrafficPolicyRemoveFunc(ctx, input)
	}
	return nil
}

func (m *MockPolicyAPI) ExecPolicyCreate(
	ctx context.Context,
	input inputs.VMToVMExecPolicyCreateInput,
) (*results.VMToVMExecPolicy, error) {
	if m.ExecPolicyCreateFunc != nil {
		return m.ExecPolicyCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) ExecPolicyList(ctx context.Context) ([]*results.VMToVMExecPolicy, error) {
	if m.ExecPolicyListFunc != nil {
		return m.ExecPolicyListFunc(ctx)
	}
	return nil, nil
}

func (m *MockPolicyAPI) ExecPolicyRemove(ctx context.Context, input inputs.VMToVMExecPolicyInput) error {
	if m.ExecPolicyRemoveFunc != nil {
		return m.ExecPolicyRemoveFunc(ctx, input)
	}
	return nil
}

// MockUpdateAPI implements api.UpdateAPI for testing.
type MockUpdateAPI struct {
	SelfUpdateCheckFunc func(ctx context.Context) (*results.UpdateCheckResult, error)
	SelfUpdateApplyFunc func(ctx context.Context, force bool) error
}

func (m *MockUpdateAPI) SelfUpdateCheck(ctx context.Context) (*results.UpdateCheckResult, error) {
	if m.SelfUpdateCheckFunc != nil {
		return m.SelfUpdateCheckFunc(ctx)
	}
	return nil, nil
}

func (m *MockUpdateAPI) SelfUpdateApply(ctx context.Context, force bool) error {
	if m.SelfUpdateApplyFunc != nil {
		return m.SelfUpdateApplyFunc(ctx, force)
	}
	return nil
}

func (m *MockPolicyAPI) LabelTrafficPolicyCreate(
	ctx context.Context, input inputs.LabelTrafficPolicyCreateInput,
) (results.LabelTrafficPolicy, error) {
	if m.LabelTrafficPolicyCreateFunc != nil {
		return m.LabelTrafficPolicyCreateFunc(ctx, input)
	}
	return results.LabelTrafficPolicy{}, nil
}

func (m *MockPolicyAPI) LabelTrafficPolicyList(ctx context.Context) ([]results.LabelTrafficPolicy, error) {
	if m.LabelTrafficPolicyListFunc != nil {
		return m.LabelTrafficPolicyListFunc(ctx)
	}
	return nil, nil
}

func (m *MockPolicyAPI) LabelTrafficPolicyRemove(ctx context.Context, input inputs.LabelTrafficPolicyInput) error {
	if m.LabelTrafficPolicyRemoveFunc != nil {
		return m.LabelTrafficPolicyRemoveFunc(ctx, input)
	}
	return nil
}
