package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Rationale: the private relay command must not reintroduce a second path
// selection surface for fixed managed artifacts.
func TestConsoleRelayDoesNotExposeManagedFilenameOverrides(t *testing.T) {
	command := newConsoleRelayCmd()

	for _, name := range []string{"pid-filename", "socket-filename", "log-filename"} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, command.Flags().Lookup(name))
		})
	}
}
