package testutil

import (
	"context"

	"mvmctl/internal/infra/event"
	"mvmctl/pkg/api/inputs"
)

// MockSSHAPI implements api.SSHAPI for testing.
type MockSSHAPI struct {
	SSHConnectFunc func(ctx context.Context, input inputs.SSHInput, onProgress event.OnProgressCallback) error
}

func (m *MockSSHAPI) SSHConnect(ctx context.Context, input inputs.SSHInput, onProgress event.OnProgressCallback) error {
	if m.SSHConnectFunc != nil {
		return m.SSHConnectFunc(ctx, input, onProgress)
	}
	return nil
}
