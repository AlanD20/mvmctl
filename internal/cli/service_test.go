package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
)

// Rationale: privileged Jailer operations use the authenticated root receiver;
// the public run command must not expose a second Jailer entry point.
func TestRunCommandDoesNotExposeJailerService(t *testing.T) {
	root := cli.NewRootCmd(nil)
	run, _, err := root.Find([]string{"run"})
	require.NoError(t, err)
	require.Equal(t, "run", run.Name())

	for _, child := range run.Commands() {
		assert.NotEqual(t, "jailer", child.Name())
		assert.NotContains(t, child.Aliases, "jailer")
	}
}
