package testutil

import (
	"context"

	"mvmctl/pkg/api/inputs"
)

// MockLogAPI implements api.LogAPI for testing.
type MockLogAPI struct {
	LogStreamFunc         func(ctx context.Context, input inputs.LogInput, callback func(string) error) error
	LogStreamChannelFunc  func(ctx context.Context, input inputs.LogInput) (lineCh <-chan string, errCh <-chan error, err error)
}

func (m *MockLogAPI) LogStream(ctx context.Context, input inputs.LogInput, callback func(string) error) error {
	if m.LogStreamFunc != nil {
		return m.LogStreamFunc(ctx, input, callback)
	}
	return nil
}

func (m *MockLogAPI) LogStreamChannel(ctx context.Context, input inputs.LogInput) (lineCh <-chan string, errCh <-chan error, err error) {
	if m.LogStreamChannelFunc != nil {
		return m.LogStreamChannelFunc(ctx, input)
	}
	return nil, nil, nil
	}
