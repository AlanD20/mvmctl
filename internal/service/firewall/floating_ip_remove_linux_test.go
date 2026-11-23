package firewall

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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

// Rationale: reboot preserves logical owner history but clears endpoints and keys.
// Network recovery may observe that history; only admitted lifecycle retirement
// can replace it with the exact new ownerless generation, without reactivation.
func TestDormantFloatingIPRetirementPreservesHistoryAndNeverActivates(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "finite", true: "persistent"}[persistent], func(t *testing.T) {
			state := floatingLeaseTestState(t)
			if persistent {
				state.intent.FloatingIPs[0].Lifetime = FloatingIPPersistent
				state.intent.FloatingIPs[0].LeaseDuration = 0
			}
			old := state.intent.FloatingIPs[0]
			fenced := ownerlessFloatingLease(old)
			fenced.Generation = 8
			state.intent.Endpoints = nil
			var err error
			state.compiled, err = state.intent.compile()
			require.NoError(t, err)
			observed := kernelObservationFromCompiled(state.compiled)
			before := cloneIntent(state.intent)
			require.NoError(t, state.verifyDormantFloatingIP([]FloatingIPBinding{old}, fenced, observed))
			require.Equal(t, old, state.intent.FloatingIPs[0], "projection must preserve historical ownership")
			target, err := state.priorBootFloatingIPRetirementIntent([]FloatingIPBinding{old}, fenced, observed)
			require.NoError(t, err)
			if diff := cmp.Diff(before, state.intent, cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})); diff != "" {
				t.Fatalf("retirement planning mutated source history (-want +got):\n%s", diff)
			}
			require.Equal(t, fenced, target.FloatingIPs[0])
			compiled, err := target.compile()
			require.NoError(t, err)
			require.Empty(t, compiled.bridge.secondaryOwners, "no alias/key owner may be activated")
			state.intent, state.compiled = target, compiled
			observed = kernelObservationFromCompiled(compiled)
			require.NoError(t, state.verifyRetiredFloatingIP(fenced, observed))
			_, err = state.priorBootFloatingIPRetirementIntent([]FloatingIPBinding{old}, fenced, observed)
			require.Error(t, err, "a stale owned root record cannot adopt a newer fenced generation")
			retry, err := state.priorBootFloatingIPRetirementIntent([]FloatingIPBinding{old, fenced}, fenced, observed)
			require.NoError(t, err, "the persisted recovery record explicitly permits its exact completed fence")
			require.Equal(t, fenced, retry.FloatingIPs[0])
		})
	}
}

func TestDormantFloatingIPRetirementRefusesActiveOrReboundAuthority(t *testing.T) {
	for _, name := range []string{"live endpoint", "foreign owner", "newer generation", "old generation",
		"foreign resource", "rebound address", "missing resource", "current key", "unlisted fence"} {
		t.Run(name, func(t *testing.T) {
			state := floatingLeaseTestState(t)
			old := state.intent.FloatingIPs[0]
			fenced := ownerlessFloatingLease(old)
			fenced.Generation++
			if name != "live endpoint" {
				state.intent.Endpoints = nil
			}
			switch name {
			case "foreign owner":
				reservation := state.intent.Reservations[0]
				reservation.VMID = strings.Repeat("9", 32)
				reservation.IPv4 = netip.MustParseAddr("10.0.0.3")
				reservation.MAC[5]++
				state.intent.Reservations = append(state.intent.Reservations, reservation)
				state.intent.FloatingIPs[0].OwnerVMID = reservation.VMID
			case "newer generation":
				state.intent.FloatingIPs[0].Generation++
			case "old generation":
				state.intent.FloatingIPs[0].Generation--
			case "foreign resource":
				state.intent.FloatingIPs[0].ID = strings.Repeat("9", 32)
			case "rebound address":
				state.intent.FloatingIPs[0].IPv4 = netip.MustParseAddr("10.0.0.101")
			case "missing resource":
				state.intent.FloatingIPs = nil
			case "unlisted fence":
				state.intent.FloatingIPs[0] = fenced
			}
			var err error
			state.compiled, err = state.intent.compile()
			require.NoError(t, err)
			observed := kernelObservationFromCompiled(state.compiled)
			if name == "current key" {
				observed.tables[0].secondaryElements = []secondaryLeaseElement{
					{
						key:       secondaryLiveKey(),
						lifetime:  FloatingIPFinite,
						timeout:   5 * time.Second,
						remaining: time.Second,
					},
				}
			}
			before := cloneIntent(state.intent)
			_, err = state.priorBootFloatingIPRetirementIntent([]FloatingIPBinding{old}, fenced, observed)
			require.Error(t, err)
			if diff := cmp.Diff(before, state.intent, cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})); diff != "" {
				t.Fatalf("refused retirement mutated history (-want +got):\n%s", diff)
			}
		})
	}
}
