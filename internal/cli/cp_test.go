package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// ─── NewCpCmd ──────────────────────────────────────────────────────────────
// Rationale: CP is a leaf command wrapping CPAPI.CPCopy with a rich progress
// display. Verify the command shell is created correctly with all expected
// flags and basic arg validation.

func TestNewCpCmd(t *testing.T) {
	mock := &testutil.MockCPAPI{}
	cmd := cli.NewCpCmd(mock)

	assert.Contains(t, cmd.Use, "cp", "command must contain 'cp'")
	assert.Equal(t, "Copy files between host and VM", cmd.Short)
	assert.NotNil(t, cmd.RunE, "RunE must be set")
	assert.True(t, cmd.SilenceErrors, "SilenceErrors must be true for cp")

	// Verify expected flags
	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{name: "user", shorthand: "u"},
		{name: "key", shorthand: ""},
		{name: "force", shorthand: "f"},
	}

	for _, f := range expectedFlags {
		t.Run("flag_"+f.name, func(t *testing.T) {
			flag := cmd.Flags().Lookup(f.name)
			require.NotNil(t, flag, "flag --%s must exist", f.name)
			if f.shorthand != "" {
				shorthand := cmd.Flags().ShorthandLookup(f.shorthand)
				require.NotNil(t, shorthand, "shorthand -%s must exist", f.shorthand)
			}
		})
	}

	// No subcommands (leaf command)
	assert.Empty(t, cmd.Commands(), "cp should have no subcommands")
}

// ─── CP validation ─────────────────────────────────────────────────────────
// Rationale: CP requires at least two arguments (source and target). A broken
// validation would allow invalid invocations that result in confusing errors.

func TestNewCpCmd_Validation(t *testing.T) {
	t.Run("less_than_two_args_returns_error", func(t *testing.T) {
		mock := &testutil.MockCPAPI{}
		cmd := cli.NewCpCmd(mock)
		err := cmd.RunE(cmd, []string{"source.txt"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least two arguments required")
	})

	t.Run("no_args_returns_error", func(t *testing.T) {
		mock := &testutil.MockCPAPI{}
		cmd := cli.NewCpCmd(mock)
		err := cmd.RunE(cmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least two arguments required")
	})

	t.Run("successful_copy", func(t *testing.T) {
		mock := &testutil.MockCPAPI{
			CPCopyFunc: func(_ context.Context, input inputs.CPInput, _ event.OnDownloadCallback) (*results.CPCopyResult, error) {
				return &results.CPCopyResult{Bytes: 1024, Message: "Copy completed"}, nil
			},
		}
		cmd := cli.NewCpCmd(mock)
		err := cmd.RunE(cmd, []string{"source.txt", "my-vm:/tmp/"})
		assert.NoError(t, err)
	})

	t.Run("cp_error_propagates", func(t *testing.T) {
		mock := &testutil.MockCPAPI{
			CPCopyFunc: func(_ context.Context, input inputs.CPInput, _ event.OnDownloadCallback) (*results.CPCopyResult, error) {
				return nil, errors.New("connection refused")
			},
		}
		cmd := cli.NewCpCmd(mock)
		err := cmd.RunE(cmd, []string{"source.txt", "my-vm:/tmp/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection refused")
	})
}
