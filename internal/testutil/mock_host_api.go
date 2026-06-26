package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/results"
)

// MockHostAPI implements api.HostAPI for testing.
type MockHostAPI struct {
	HostInitFunc                  func(ctx context.Context, onProgress event.OnProgressCallback) (any, error)
	HostGetStateFunc              func(ctx context.Context) (*model.HostStateItem, error)
	HostDetectResourcesFunc       func(ctx context.Context) (*model.HostResources, error)
	HostNetworkSetupFunc          func(ctx context.Context) error
	HostInfoFunc                  func(ctx context.Context) (*results.HostInfo, error)
	HostRefreshCapacityFunc       func(ctx context.Context) (*results.HostInfo, error)
	HostCheckKVMAccessFunc        func() bool
	HostCheckRequiredBinariesFunc func() []string
	HostGetIPForwardStatusFunc    func(ctx context.Context) (string, error)
	HostStatusCheckFunc           func(ctx context.Context) *results.HostStatusCheck
	HostCleanFunc                 func(ctx context.Context) ([]string, error)
	HostResetFunc                 func(ctx context.Context) ([]string, error)
	HostGetRunningVMsFunc         func(ctx context.Context) ([]*model.VMItem, error)
	HostIsInitializedFunc         func(ctx context.Context) bool
	HostCheckReadinessFunc        func(ctx context.Context) *model.ProbeResult
}

func (m *MockHostAPI) HostInit(ctx context.Context, onProgress event.OnProgressCallback) (any, error) {
	if m.HostInitFunc != nil {
		return m.HostInitFunc(ctx, onProgress)
	}
	return nil, nil
}

func (m *MockHostAPI) HostGetState(ctx context.Context) (*model.HostStateItem, error) {
	if m.HostGetStateFunc != nil {
		return m.HostGetStateFunc(ctx)
	}
	return nil, nil
}

func (m *MockHostAPI) HostDetectResources(ctx context.Context) (*model.HostResources, error) {
	if m.HostDetectResourcesFunc != nil {
		return m.HostDetectResourcesFunc(ctx)
	}
	return nil, nil
}

func (m *MockHostAPI) HostNetworkSetup(ctx context.Context) error {
	if m.HostNetworkSetupFunc != nil {
		return m.HostNetworkSetupFunc(ctx)
	}
	return nil
}

func (m *MockHostAPI) HostInfo(ctx context.Context) (*results.HostInfo, error) {
	if m.HostInfoFunc != nil {
		return m.HostInfoFunc(ctx)
	}
	return nil, nil
}

func (m *MockHostAPI) HostRefreshCapacity(ctx context.Context) (*results.HostInfo, error) {
	if m.HostRefreshCapacityFunc != nil {
		return m.HostRefreshCapacityFunc(ctx)
	}
	return nil, nil
}

func (m *MockHostAPI) HostCheckKVMAccess() bool {
	if m.HostCheckKVMAccessFunc != nil {
		return m.HostCheckKVMAccessFunc()
	}
	return false
}

func (m *MockHostAPI) HostCheckRequiredBinaries() []string {
	if m.HostCheckRequiredBinariesFunc != nil {
		return m.HostCheckRequiredBinariesFunc()
	}
	return nil
}

func (m *MockHostAPI) HostGetIPForwardStatus(ctx context.Context) (string, error) {
	if m.HostGetIPForwardStatusFunc != nil {
		return m.HostGetIPForwardStatusFunc(ctx)
	}
	return "", nil
}

func (m *MockHostAPI) HostStatusCheck(ctx context.Context) *results.HostStatusCheck {
	if m.HostStatusCheckFunc != nil {
		return m.HostStatusCheckFunc(ctx)
	}
	return nil
}

func (m *MockHostAPI) HostClean(ctx context.Context) ([]string, error) {
	if m.HostCleanFunc != nil {
		return m.HostCleanFunc(ctx)
	}
	return nil, nil
}

func (m *MockHostAPI) HostReset(ctx context.Context) ([]string, error) {
	if m.HostResetFunc != nil {
		return m.HostResetFunc(ctx)
	}
	return nil, nil
}

func (m *MockHostAPI) HostGetRunningVMs(ctx context.Context) ([]*model.VMItem, error) {
	if m.HostGetRunningVMsFunc != nil {
		return m.HostGetRunningVMsFunc(ctx)
	}
	return nil, nil
}

func (m *MockHostAPI) HostIsInitialized(ctx context.Context) bool {
	if m.HostIsInitializedFunc != nil {
		return m.HostIsInitializedFunc(ctx)
	}
	return false
}

func (m *MockHostAPI) HostCheckReadiness(ctx context.Context) *model.ProbeResult {
	if m.HostCheckReadinessFunc != nil {
		return m.HostCheckReadinessFunc(ctx)
	}
	return nil
}
