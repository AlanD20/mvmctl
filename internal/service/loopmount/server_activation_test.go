package loopmount

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rationale: The production constructor is the only authorized activation seam. It must close over the exact system
// authority, VM-provision, and read-only inspection adapters while leaving image optimization unconstructed.
func TestNewProvisionInspectServerConstructsOnlyAuthorizedEffects(t *testing.T) {
	generatedAgent := []byte{0x7f, 'E', 'L', 'F', 1}
	accessorCalls := 0
	server := NewProvisionInspectServer(func(ctx context.Context) ([]byte, error) {
		accessorCalls++
		assert.Same(t, t.Context(), ctx)
		return bytes.Clone(generatedAgent), nil
	})

	require.NotNil(t, server)
	require.NoError(t, server.validateRootfsEffectsDependencies())
	assert.Nil(t, server.optimizeEffects)
	effects, ok := server.effects.(*provisionInspectRootfsEffects)
	require.True(t, ok)
	require.NotNil(t, effects.provision)
	require.NotNil(t, effects.inspect)
	require.NoError(t, validateRootfsVMProvisionDeps(effects.provision.deps))
	require.NoError(t, validateRootfsImageInspectDeps(effects.inspect.deps))
	gotAgent, err := effects.provision.deps.agentBinary(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, gotAgent)
	if diff := cmp.Diff(generatedAgent, gotAgent); diff != "" {
		t.Errorf("embedded agent mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 1, accessorCalls)
}

// Rationale: Image optimization is a distinct production receiver with durable shrink authority.
func TestNewImageShrinkServerConstructsDurableOptimizeEffect(t *testing.T) {
	server := NewImageShrinkServer()
	require.NotNil(t, server)
	require.NoError(t, server.validateImageOptimizeDependencies())
	assert.Nil(t, server.effects)
	assert.True(t, server.durableImageShrink)
	adapter, ok := server.optimizeEffects.(*rootfsImageShrinkAdapter)
	require.True(t, ok)
	require.NotNil(t, adapter)
	require.NoError(t, validateRootfsVMProvisionDeps(adapter.deps))
}
