package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// MockCPAPI implements api.CPAPI for testing.
type MockCPAPI struct {
	CPCopyFunc func(ctx context.Context, input inputs.CPInput, onProgress event.OnDownloadCallback) (*results.CPCopyResult, error)
}

func (m *MockCPAPI) CPCopy(
	ctx context.Context,
	input inputs.CPInput,
	onProgress event.OnDownloadCallback,
) (*results.CPCopyResult, error) {
	if m.CPCopyFunc != nil {
		return m.CPCopyFunc(ctx, input, onProgress)
	}
	return nil, nil
}
