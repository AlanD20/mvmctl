package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHostAdministratorUsesRestrictedFlagSet(t *testing.T) {
	got, err := ParseHostAdministrator([]string{"--json", "host", "reset", "--operator=alice", "--force"})
	require.NoError(t, err)
	assert.Equal(t, HostAdministratorOptions{Action: "reset", Operator: "alice", Force: true, JSON: true}, got)

	help, err := ParseHostAdministrator([]string{"host", "init", "--help"})
	require.NoError(t, err)
	assert.True(t, help.Help)
	assert.NotContains(t, help.Usage, "--force")

	for _, args := range [][]string{
		{"host", "init", "--force"},
		{"host", "reset"},
		{"host", "init", "--operator", "--json"},
		{"host", "init", "--operator", "alice", "--operator", "bob"},
		{"host", "--", "init"},
	} {
		assert.True(t, IsHostAdministratorInvocation(args), args)
		_, err := ParseHostAdministrator(args)
		assert.Error(t, err, args)
	}
}
