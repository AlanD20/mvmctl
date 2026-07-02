package testutil

import (
	"context"

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
