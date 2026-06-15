package cli_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/results"
)

// ─── NewConsoleCmd ─────────────────────────────────────────────────────────
// Rationale: NewConsoleCmd is the entry point for console operations.
// Missing flags silently disable state/kill/attach without error.

func TestNewConsoleCmd(t *testing.T) {
	mock := &testutil.MockConsoleAPI{}
	cmd := cli.NewConsoleCmd(mock)

	assert.Contains(t, cmd.Use, "console", "command must contain 'console'")
	assert.Equal(t, "VM console access", cmd.Short)

	// Console has flags but no subcommands
	subcommands := cmd.Commands()
	assert.Empty(t, subcommands, "console should have no subcommands")

	// Verify flags are registered
	stateFlag := cmd.Flags().Lookup("state")
	require.NotNil(t, stateFlag, "--state flag must exist")
	killFlag := cmd.Flags().Lookup("kill")
	require.NotNil(t, killFlag, "--kill flag must exist")
}

// ─── Console state (via --state flag) ──────────────────────────────────────
// Rationale: Console state shows whether a console relay is running for a VM.
// A broken state check leaves users unable to debug console connectivity.

func TestShowConsoleState(t *testing.T) {
	t.Run("running_relay", func(t *testing.T) {
		pid := 1234
		mock := &testutil.MockConsoleAPI{
			ConsoleGetStateFunc: func(_ context.Context, identifier string) (*results.ConsoleStateResult, error) {
				return &results.ConsoleStateResult{
					Running:    true,
					PID:        &pid,
					SocketPath: "/tmp/vm-console.sock",
				}, nil
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetArgs([]string{"--state", "test-vm"})
		err := cmd.Execute()
		assert.NoError(t, err)
	})

	t.Run("stopped_relay", func(t *testing.T) {
		mock := &testutil.MockConsoleAPI{
			ConsoleGetStateFunc: func(_ context.Context, identifier string) (*results.ConsoleStateResult, error) {
				return &results.ConsoleStateResult{
					Running: false, PID: nil, SocketPath: "",
				}, nil
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetArgs([]string{"--state", "test-vm"})
		err := cmd.Execute()
		assert.NoError(t, err)
	})

	t.Run("api_error_propagates", func(t *testing.T) {
		mock := &testutil.MockConsoleAPI{
			ConsoleGetStateFunc: func(_ context.Context, identifier string) (*results.ConsoleStateResult, error) {
				return nil, errors.New("VM not found")
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetArgs([]string{"--state", "nonexistent-vm"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VM not found")
	})

	t.Run("context_is_propagated", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		gotCancelled := false
		mock := &testutil.MockConsoleAPI{
			ConsoleGetStateFunc: func(c context.Context, identifier string) (*results.ConsoleStateResult, error) {
				if c.Err() != nil {
					gotCancelled = true
				}
				return &results.ConsoleStateResult{Running: false}, nil
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetContext(ctx)
		cmd.SetArgs([]string{"--state", "test-vm"})
		err := cmd.Execute()
		assert.NoError(t, err)
		assert.True(t, gotCancelled, "cancelled context should be visible to mock")
	})

	t.Run("pid_zero_is_omitted", func(t *testing.T) {
		pid := 0
		mock := &testutil.MockConsoleAPI{
			ConsoleGetStateFunc: func(_ context.Context, identifier string) (*results.ConsoleStateResult, error) {
				return &results.ConsoleStateResult{
					Running: false, PID: &pid, SocketPath: "",
				}, nil
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetArgs([]string{"--state", "test-vm"})
		err := cmd.Execute()
		assert.NoError(t, err)
	})
}

// ─── Console kill (via --kill flag) ────────────────────────────────────────
// Rationale: Console kill stops a console relay. A broken kill command leaves
// orphaned relay processes consuming resources.

func TestKillConsoleRelay(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mock := &testutil.MockConsoleAPI{
			ConsoleKillFunc: func(_ context.Context, identifier string) error {
				return nil
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetArgs([]string{"--kill", "test-vm"})
		err := cmd.Execute()
		assert.NoError(t, err)
	})

	t.Run("error_propagates", func(t *testing.T) {
		mock := &testutil.MockConsoleAPI{
			ConsoleKillFunc: func(_ context.Context, identifier string) error {
				return errors.New("relay not running")
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetArgs([]string{"--kill", "test-vm"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "relay not running")
	})
}

// ─── Console attach (via position arg, no flags) ───────────────────────────
// Rationale: Console attach connects to the VM serial console. A broken attach
// command prevents users from accessing the VM console for debugging.

func TestAttachToConsole(t *testing.T) {
	t.Run("connection_info_error_propagates", func(t *testing.T) {
		mock := &testutil.MockConsoleAPI{
			ConsoleGetConnectionInfoFunc: func(_ context.Context, identifier string) (*model.ConsoleConnectionInfo, error) {
				return nil, errors.New("VM not found")
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetArgs([]string{"nonexistent-vm"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VM not found")
	})

	t.Run("success_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockConsoleAPI{
			ConsoleGetConnectionInfoFunc: func(_ context.Context, identifier string) (*model.ConsoleConnectionInfo, error) {
				return &model.ConsoleConnectionInfo{
					SocketPath: "/tmp/test-console.sock",
					VMName:     "test-vm",
					VMID:       "vm-1",
				}, nil
			},
			ConsoleAttachConsoleFunc: func(_ context.Context, socketPath string, _ io.Reader, _ io.Writer) error {
				return nil
			},
		}
		cmd := cli.NewConsoleCmd(mock)
		cmd.SetArgs([]string{"test-vm"})
		err := cmd.Execute()
		assert.NoError(t, err)
	})
}
