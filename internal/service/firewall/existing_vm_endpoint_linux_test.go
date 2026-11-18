package firewall

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The private metadata predicate does not manufacture a ready mutation lease. The public verifier
// additionally requires genuine retained authority and complete fresh kernel readback.
func TestExistingVMEndpointRequiresExactRetainedActiveProjection(t *testing.T) {
	for _, name := range []string{"exact", "missing active endpoint", "stopped reservation only", "other operator",
		"other VM", "other network", "other boot", "other namespace", "replaced host veth", "different primary"} {
		t.Run(name, func(t *testing.T) {
			intent := secondaryDurableIntent()
			binding := intent.Endpoints[0]
			state := &mutationLeaseState{intent: intent, binding: binding}
			switch name {
			case "missing active endpoint":
				state.intent.Endpoints[0].HostVeth.IfIndex++
			case "stopped reservation only":
				state.intent.Endpoints = nil
			case "other operator":
				binding.OwnerUID++
			case "other VM":
				binding.VMID = "other"
			case "other network":
				binding.NetworkID = "other"
			case "other boot":
				binding.BootID = "other"
			case "other namespace":
				binding.HostNamespace.Inode++
			case "replaced host veth":
				binding.HostVeth.IfIndex++
			case "different primary":
				binding.IPv4 = binding.IPv4.Next()
			}
			err := state.requireExistingVMEndpoint(binding)
			if name == "exact" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestExistingVMEndpointPublicVerificationRejectsUnavailableAuthority(t *testing.T) {
	binding := secondaryDurableIntent().Endpoints[0]
	var lease *MutationLease
	require.Error(t, lease.VerifyExistingVMEndpoint(t.Context(), binding))
	lease = &MutationLease{state: &mutationLeaseState{released: true}}
	require.Error(t, lease.VerifyExistingVMEndpoint(t.Context(), binding))
	require.Error(t, lease.VerifyExistingVMEndpoint(nil, binding))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, lease.VerifyExistingVMEndpoint(ctx, binding))
}
