package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/results"
)

// MockInitAPI implements api.InitAPI for testing.
type MockInitAPI struct {
	InitCheckReadinessFunc func(ctx context.Context) *model.ProbeResult
	InitSetupHostFunc      func(ctx context.Context) error
	InitRunFunc            func(ctx context.Context, skipHost bool, skipNetwork bool, nonInteractive bool, sudoCompleted bool, downloadVersion string, onProgress event.OnProgressCallback) *results.InitResult
	InitRunFullFunc        func(ctx context.Context, skipHost bool, skipNetwork bool, nonInteractive bool, sudoCompleted bool, hostSetupMessage string, downloadVersion string, guestfsEnabled *bool, onProgress event.OnProgressCallback) *results.InitResult
}

func (m *MockInitAPI) InitCheckReadiness(ctx context.Context) *model.ProbeResult {
	if m.InitCheckReadinessFunc != nil {
		return m.InitCheckReadinessFunc(ctx)
	}
	return nil
}

func (m *MockInitAPI) InitSetupHost(ctx context.Context) error {
	if m.InitSetupHostFunc != nil {
		return m.InitSetupHostFunc(ctx)
	}
	return nil
}

func (m *MockInitAPI) InitRun(ctx context.Context, skipHost bool, skipNetwork bool, nonInteractive bool, sudoCompleted bool, downloadVersion string, onProgress event.OnProgressCallback) *results.InitResult {
	if m.InitRunFunc != nil {
		return m.InitRunFunc(ctx, skipHost, skipNetwork, nonInteractive, sudoCompleted, downloadVersion, onProgress)
	}
	return nil
}

func (m *MockInitAPI) InitRunFull(ctx context.Context, skipHost bool, skipNetwork bool, nonInteractive bool, sudoCompleted bool, hostSetupMessage string, downloadVersion string, guestfsEnabled *bool, onProgress event.OnProgressCallback) *results.InitResult {
	if m.InitRunFullFunc != nil {
		return m.InitRunFullFunc(ctx, skipHost, skipNetwork, nonInteractive, sudoCompleted, hostSetupMessage, downloadVersion, guestfsEnabled, onProgress)
	}
	return nil
}
