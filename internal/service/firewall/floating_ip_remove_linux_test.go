package firewall

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFloatingIPRemovalRequiresFencingAndRemovesOnlyDestinationPolicies(t *testing.T) {
	intent := floatingPolicyIntent()
	compiled, err := intent.compile()
	require.NoError(t, err)
	state := &mutationLeaseState{intent: intent, compiled: compiled, binding: intent.Endpoints[0]}
	binding := intent.FloatingIPs[0]
	observed := kernelObservationFromCompiled(compiled)
	_, err = state.floatingIPRemovalIntent(ownerlessFloatingLease(binding), observed)
	require.Error(t, err, "owned metadata must be fenced before removal")
	intent.FloatingIPs[0] = ownerlessFloatingLease(binding)
	state.intent = intent
	state.compiled, err = intent.compile()
	require.NoError(t, err)
	observed = kernelObservationFromCompiled(state.compiled)
	target, err := state.floatingIPRemovalIntent(intent.FloatingIPs[0], observed)
	require.NoError(t, err)
	for _, resource := range target.FloatingIPs {
		require.NotEqual(t, binding.ID, resource.ID)
	}
	for _, grant := range target.VMFloatingIPGrants {
		require.NotEqual(t, binding.ID, grant.DestinationFloatingIPID)
	}
	for _, grant := range target.NetworkFloatingIPGrants {
		require.NotEqual(t, binding.ID, grant.DestinationFloatingIPID)
	}
	require.Len(t, target.FloatingIPs, len(intent.FloatingIPs)-1)
	observed.tables[0].secondaryElements = []secondaryLeaseElement{
		{key: secondaryLiveKey(), lifetime: FloatingIPFinite, timeout: 5 * time.Second, remaining: time.Second},
	}
	_, err = state.floatingIPRemovalIntent(intent.FloatingIPs[0], observed)
	require.Error(t, err)
}
func TestFloatingIPVMRemovalRefusesOwnedReferences(t *testing.T) {
	intent := secondaryDurableIntent()
	require.Error(t, requireFloatingIPVMOwnerAbsent(intent, intent.FloatingIPs[0].OwnerVMID))
	intent.FloatingIPs[0] = ownerlessFloatingLease(intent.FloatingIPs[0])
	require.NoError(t, requireFloatingIPVMOwnerAbsent(intent, intent.Reservations[0].VMID))
}

func TestFloatingIPPriorBootRetirementRequiresExactCurrentAbsence(t *testing.T) {
	state := floatingLeaseTestState(t)
	binding := ownerlessFloatingLease(state.intent.FloatingIPs[0])
	observed := kernelObservationFromCompiled(state.compiled)
	require.Error(t, state.verifyRetiredFloatingIP(binding, observed), "owned logical authority cannot retire")
	state.intent.FloatingIPs = nil
	var err error
	state.compiled, err = state.intent.compile()
	require.NoError(t, err)
	observed = kernelObservationFromCompiled(state.compiled)
	require.NoError(t, state.verifyRetiredFloatingIP(binding, observed),
		"historical resource may be absent in exact fresh graph")
	binding.OwnerUID++
	require.Error(t, state.verifyRetiredFloatingIP(binding, observed))
}

func TestFloatingIPNetworkRemovalRequiresResourcesRetiredAndClearsSourcePolicies(t *testing.T) {
	intent := floatingPolicyIntent()
	require.Error(t, requireFloatingIPNetworkAbsent(intent, intent.FloatingIPs[0].NetworkID))
	source := intent.NetworkFloatingIPGrants[0].SourceNetworkID
	other := intent.NetworkFloatingIPGrants[0]
	other.ID = "other"
	other.SourceNetworkID = "other-network"
	intent.NetworkFloatingIPGrants = append(intent.NetworkFloatingIPGrants, other)
	removeFloatingIPNetworkSourcePolicies(&intent, source)
	require.Len(t, intent.NetworkFloatingIPGrants, 1)
	require.Equal(t, other, intent.NetworkFloatingIPGrants[0])
	intent.FloatingIPs = nil
	require.NoError(t, requireFloatingIPNetworkAbsent(intent, source))
}
