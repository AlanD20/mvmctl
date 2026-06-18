package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/testutil"
)

// --- NewInitCmd ---
// Rationale: Init is a complex wizard command that orchestrates multiple
// APIs (InitAPI, HostAPI). Verify the command shell is created correctly
// with all expected flags registered.

func TestNewInitCmd(t *testing.T) {
	mock := &testutil.MockInitAPI{}
	cmd := cli.NewInitCmd(mock, &testutil.MockHostAPI{})

	assert.Equal(t, "init", cmd.Use, "command must be 'init'")
	assert.Contains(t, cmd.Short, "Initialize", "short description should mention initialization")
	assert.NotNil(t, cmd.RunE, "RunE must be set")

	// Verify expected flags
	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{name: "non-interactive", shorthand: ""},
		{name: "skip-host", shorthand: ""},
		{name: "skip-network", shorthand: ""},
	}

	for _, f := range expectedFlags {
		t.Run("flag_"+f.name, func(t *testing.T) {
			flag := cmd.Flags().Lookup(f.name)
			require.NotNil(t, flag, "flag --%s must exist", f.name)
		})
	}

	// No subcommands (leaf command)
	assert.Empty(t, cmd.Commands(), "init should have no subcommands")

	// Verify default values
	nonInteractive := cmd.Flags().Lookup("non-interactive")
	require.NotNil(t, nonInteractive)
	assert.Equal(t, "false", nonInteractive.Value.String(), "--non-interactive defaults to false")

	skipHost := cmd.Flags().Lookup("skip-host")
	require.NotNil(t, skipHost)
	assert.Equal(t, "false", skipHost.Value.String(), "--skip-host defaults to false")

	skipNetwork := cmd.Flags().Lookup("skip-network")
	require.NotNil(t, skipNetwork)
	assert.Equal(t, "false", skipNetwork.Value.String(), "--skip-network defaults to false")
}
