package testutil

import (
	"context"
	"sync"

	"mvmctl/internal/lib/system"
)

// FakeRunner implements system.CommandRunner for testing.
// Records all calls and returns canned results.
type FakeRunner struct {
	mu sync.Mutex
	// Calls stores every Run/Stream invocation for test assertions
	Calls []FakeCall
	// StubRunResult is returned by Run() when set
	StubRunResult *system.RunResult
	// StubRunErr is returned by Run() when set
	StubRunErr error
}

// FakeCall records a single invocation of Run or Stream.
type FakeCall struct {
	Args []string
}

// Run records the call and returns the stubbed result or error.
func (f *FakeRunner) Run(ctx context.Context, args []string, opts ...system.RunOption) (*system.RunResult, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, FakeCall{Args: append([]string{}, args...)})
	f.mu.Unlock()
	if f.StubRunErr != nil {
		return f.StubRunResult, f.StubRunErr
	}
	if f.StubRunResult != nil {
		return f.StubRunResult, nil
	}
	return &system.RunResult{ExitCode: 0, Stdout: "", Stderr: ""}, nil
}

// Stream records the call and returns an immediately-closed channel.
func (f *FakeRunner) Stream(
	ctx context.Context,
	args []string,
	opts ...system.RunOption,
) (<-chan system.StreamLine, error) {
	ch := make(chan system.StreamLine)
	close(ch)
	return ch, nil
}
