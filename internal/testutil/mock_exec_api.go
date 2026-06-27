package testutil

import (
	"context"

	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// MockExecAPI implements api.ExecAPI for testing.
type MockExecAPI struct {
	ExecFunc func(ctx context.Context, input inputs.ExecInput) (*results.ExecResult, error)
}

func (m *MockExecAPI) Exec(ctx context.Context, input inputs.ExecInput) (*results.ExecResult, error) {
	if m.ExecFunc != nil {
		return m.ExecFunc(ctx, input)
	}
	return nil, nil
}
