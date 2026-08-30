package loopmount

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rationale: The production constructor is the only authorized activation seam. It must close over the exact system
// authority, VM-provision, and read-only inspection adapters while leaving image optimization unconstructed.
func TestNewProvisionInspectServerConstructsOnlyAuthorizedEffects(t *testing.T) {
	server := NewProvisionInspectServer()

	require.NotNil(t, server)
	require.NoError(t, server.validateRootfsEffectsDependencies())
	assert.Nil(t, server.optimizeEffects)
	effects, ok := server.effects.(*provisionInspectRootfsEffects)
	require.True(t, ok)
	assert.NotNil(t, effects.provision)
	assert.NotNil(t, effects.inspect)
	assert.NotNil(t, effects.provision.deps.agentBinary)
}
