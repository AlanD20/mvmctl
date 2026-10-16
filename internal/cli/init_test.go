package cli_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/results"
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
		{name: "binary-version", shorthand: ""},
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

	binaryVersion := cmd.Flags().Lookup("binary-version")
	require.NotNil(t, binaryVersion)
	assert.Empty(t, binaryVersion.Value.String(), "--binary-version defaults to empty")
}

func TestInitCommandForwardsBinaryVersion(t *testing.T) {
	tests := map[string]struct {
		args               []string
		wantVersion        string
		wantNonInteractive bool
	}{
		"explicit_version": {
			args:        []string{"--skip-host", "--skip-network", "--binary-version", "1.15.2"},
			wantVersion: "1.15.2",
		},
		"non_interactive": {
			args: []string{
				"--non-interactive", "--skip-host", "--skip-network", "--binary-version", "latest",
			},
			wantVersion:        "latest",
			wantNonInteractive: true,
		},
		"no_version_flag": {
			args: []string{"--skip-host", "--skip-network"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			calls := 0
			mock := &testutil.MockInitAPI{
				InitRunFullFunc: func(
					_ context.Context,
					_, _, nonInteractive, _ bool,
					_ string,
					downloadVersion string,
					_ *bool,
					_ event.OnProgressCallback,
				) *results.InitResult {
					calls++
					assert.Equal(t, tc.wantVersion, downloadVersion)
					assert.Equal(t, tc.wantNonInteractive, nonInteractive)
					return &results.InitResult{HostReady: true}
				},
			}
			cmd := cli.NewInitCmd(mock, &testutil.MockHostAPI{})
			cmd.SetArgs(tc.args)

			require.NoError(t, cmd.ExecuteContext(context.Background()))
			assert.Equal(t, 1, calls)
		})
	}
}
