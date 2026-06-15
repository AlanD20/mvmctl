package testutil

import (
	"context"
	"io"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/results"
)

// MockConsoleAPI implements api.ConsoleAPI for testing.
type MockConsoleAPI struct {
	ConsoleGetStateFunc          func(ctx context.Context, identifier string) (*results.ConsoleStateResult, error)
	ConsoleGetConnectionInfoFunc func(ctx context.Context, identifier string) (*model.ConsoleConnectionInfo, error)
	ConsoleKillFunc              func(ctx context.Context, identifier string) error
	ConsoleAttachConsoleFunc     func(ctx context.Context, socketPath string, stdin io.Reader, stdout io.Writer) error
}

func (m *MockConsoleAPI) ConsoleGetState(ctx context.Context, identifier string) (*results.ConsoleStateResult, error) {
	if m.ConsoleGetStateFunc != nil {
		return m.ConsoleGetStateFunc(ctx, identifier)
	}
	return nil, nil
}

func (m *MockConsoleAPI) ConsoleGetConnectionInfo(ctx context.Context, identifier string) (*model.ConsoleConnectionInfo, error) {
	if m.ConsoleGetConnectionInfoFunc != nil {
		return m.ConsoleGetConnectionInfoFunc(ctx, identifier)
	}
	return nil, nil
}

func (m *MockConsoleAPI) ConsoleKill(ctx context.Context, identifier string) error {
	if m.ConsoleKillFunc != nil {
		return m.ConsoleKillFunc(ctx, identifier)
	}
	return nil
}

func (m *MockConsoleAPI) ConsoleAttachConsole(ctx context.Context, socketPath string, stdin io.Reader, stdout io.Writer) error {
	if m.ConsoleAttachConsoleFunc != nil {
		return m.ConsoleAttachConsoleFunc(ctx, socketPath, stdin, stdout)
	}
	return nil
	}
