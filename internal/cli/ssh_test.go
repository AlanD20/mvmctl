package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/testutil"
)

// --- NewSSHCmd ---
// Rationale: SSH is a complex command with many flags. Verify the command
// shell is created correctly with all expected flags registered.

func TestNewSSHCmd(t *testing.T) {
	mock := &testutil.MockSSHAPI{}
	cmd := cli.NewSSHCmd(mock)

	assert.Contains(t, cmd.Use, "ssh", "command must contain 'ssh'")
	assert.Equal(t, "VM SSH access", cmd.Short)
	assert.NotNil(t, cmd.RunE, "RunE must be set")

	// Verify all expected flags are registered
	expectedFlags := []struct {
		name      string
		shorthand string
		typ       string
	}{
		{name: "user", shorthand: "u", typ: "string"},
		{name: "key", shorthand: "", typ: "string"},
		{name: "cmd", shorthand: "c", typ: "string"},
		{name: "timeout", shorthand: "t", typ: "int"},
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

	// Verify no subcommands (ssh is a leaf command)
	assert.Empty(t, cmd.Commands(), "ssh should have no subcommands")

	// Verify TraverseChildren is true
	assert.True(t, cmd.TraverseChildren, "TraverseChildren must be true for SSH")
}

// --- SSH no-args behavior ---
// Rationale: SSH shows help with no args. A broken no-args handler would
// produce an error instead of help, confusing users.

func TestNewSSHCmd_NoArgsShowsHelp(t *testing.T) {
	mock := &testutil.MockSSHAPI{}
	cmd := cli.NewSSHCmd(mock)

	// With no args, RunE should return cmd.Help() which shows help text
	// and returns nil (help does not error)
	err := cmd.RunE(cmd, nil)
	assert.NoError(t, err, "running ssh with no args should show help, not error")
}
