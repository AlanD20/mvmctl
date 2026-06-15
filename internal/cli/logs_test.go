package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/testutil"
)

// ─── NewLogsCmd ────────────────────────────────────────────────────────────
// Rationale: Logs is a leaf command wrapping LogAPI.LogStream. Verify the
// command shell is created correctly with all expected flags registered.

func TestNewLogsCmd(t *testing.T) {
	mock := &testutil.MockLogAPI{}
	cmd := cli.NewLogsCmd(mock)

	assert.Contains(t, cmd.Use, "logs", "command must contain 'logs'")
	assert.Equal(t, "VM log management", cmd.Short)
	assert.NotNil(t, cmd.RunE, "RunE must be set")

	// Verify expected flags
	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{name: "os", shorthand: ""},
		{name: "lines", shorthand: "n"},
		{name: "follow", shorthand: "f"},
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
	assert.Empty(t, cmd.Commands(), "logs should have no subcommands")
}

// ─── Logs no-args behavior ─────────────────────────────────────────────────
// Rationale: Logs shows help with no args. A broken no-args handler would
// produce an error instead of help, confusing users.

func TestNewLogsCmd_NoArgsShowsHelp(t *testing.T) {
	mock := &testutil.MockLogAPI{}
	cmd := cli.NewLogsCmd(mock)

	// With no args, RunE should return cmd.Help()
	err := cmd.RunE(cmd, nil)
	assert.NoError(t, err, "running logs with no args should show help, not error")
}
