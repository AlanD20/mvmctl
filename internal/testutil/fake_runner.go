package testutil

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"sync"

	"mvmctl/internal/lib/system"
)

// ── FakeRunner ──────────────────────────────────────────────────────────────

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
func (f *FakeRunner) Run(ctx context.Context, args []string, opts system.RunCmdOpts) (*system.RunResult, error) {
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
	opts system.RunCmdOpts,
) (<-chan system.StreamLine, error) {
	ch := make(chan system.StreamLine)
	close(ch)
	return ch, nil
}

// ── FakeOS ──────────────────────────────────────────────────────────────────

// FakeOS implements system.OSProvider for testing.
// Each method returns the corresponding field value.
// Zero values provide sensible defaults (non-root, no groups, etc.).
type FakeOS struct {
	GeteuidVal    int
	GetegidVal    int
	GetgidVal     int
	GetgroupsVal  []int
	LookupGroupFn func(name string) (*user.Group, error)
	CurrentFn     func() (*user.User, error)
	LookPathFn    func(file string) (string, error)
	StatFn        func(name string) (os.FileInfo, error)
	FindProcessFn func(pid int) (*os.Process, error)
}

func (f *FakeOS) Geteuid() int                      { return f.GeteuidVal }
func (f *FakeOS) Getegid() int                      { return f.GetegidVal }
func (f *FakeOS) Getgid() int                       { return f.GetgidVal }
func (f *FakeOS) Getgroups() ([]int, error)         { return f.GetgroupsVal, nil }

func (f *FakeOS) LookupGroup(name string) (*user.Group, error) {
	if f.LookupGroupFn != nil {
		return f.LookupGroupFn(name)
	}
	return nil, fmt.Errorf("group not found: %s", name)
}

func (f *FakeOS) Current() (*user.User, error) {
	if f.CurrentFn != nil {
		return f.CurrentFn()
	}
	return nil, fmt.Errorf("no current user")
}

func (f *FakeOS) LookPath(file string) (string, error) {
	if f.LookPathFn != nil {
		return f.LookPathFn(file)
	}
	return "", fmt.Errorf("not found: %s", file)
}

func (f *FakeOS) Stat(name string) (os.FileInfo, error) {
	if f.StatFn != nil {
		return f.StatFn(name)
	}
	return nil, fmt.Errorf("not found: %s", name)
}

func (f *FakeOS) FindProcess(pid int) (*os.Process, error) {
	if f.FindProcessFn != nil {
		return f.FindProcessFn(pid)
	}
	return nil, fmt.Errorf("not found: %d", pid)
}

func (f *FakeOS) IsNotExist(err error) bool {
	return os.IsNotExist(err)
}
