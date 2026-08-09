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
	PolicyCreateFunc  func(context.Context, inputs.PolicyCreateInput) (*results.Policy, error)
	PolicyListFunc    func(context.Context) ([]*results.Policy, error)
	PolicyInspectFunc func(context.Context, inputs.PolicyInput) (*results.Policy, error)
	PolicyRemoveFunc  func(context.Context, inputs.PolicyInput) error
	PolicySyncFunc    func(context.Context) (*results.PolicySync, error)
}

func (m *MockPolicyAPI) PolicyCreate(ctx context.Context, input inputs.PolicyCreateInput) (*results.Policy, error) {
	if m.PolicyCreateFunc != nil {
		return m.PolicyCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) PolicyList(ctx context.Context) ([]*results.Policy, error) {
	if m.PolicyListFunc != nil {
		return m.PolicyListFunc(ctx)
	}
	return nil, nil
}

func (m *MockPolicyAPI) PolicyInspect(ctx context.Context, input inputs.PolicyInput) (*results.Policy, error) {
	if m.PolicyInspectFunc != nil {
		return m.PolicyInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) PolicyRemove(ctx context.Context, input inputs.PolicyInput) error {
	if m.PolicyRemoveFunc != nil {
		return m.PolicyRemoveFunc(ctx, input)
	}
	return nil
}

func (m *MockPolicyAPI) PolicySync(ctx context.Context) (*results.PolicySync, error) {
	if m.PolicySyncFunc != nil {
		return m.PolicySyncFunc(ctx)
	}
	return nil, nil
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
