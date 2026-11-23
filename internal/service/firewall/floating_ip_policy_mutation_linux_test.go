package firewall

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFloatingIPTrafficPolicyMutationKeepsDormantCrossNetworkIdentityAndReceipt(t *testing.T) {
	for _, cross := range []bool{false, true} {
		t.Run(map[bool]string{false: "same network", true: "cross network"}[cross], func(t *testing.T) {
			intent := floatingPolicyIntent()
			intent.FloatingIPs[0] = ownerlessFloatingLease(intent.FloatingIPs[0])
			if cross {
				intent.FloatingIPs[0].NetworkID = intent.Networks[1].NetworkID
				intent.FloatingIPs[0].IPv4 = netip.MustParseAddr("10.1.0.100")
			}
			vmGrant, netGrant := intent.VMFloatingIPGrants[0], intent.NetworkFloatingIPGrants[0]
			vmGrant.Incarnation, netGrant.Incarnation = "", ""
			intent.VMFloatingIPGrants, intent.NetworkFloatingIPGrants = nil, nil
			state := &mutationLeaseState{binding: intent.Endpoints[0], intent: intent}
			target, vmReceipt, err := state.vmFloatingIPTrafficGrantIntent(vmGrant, "", "")
			require.NoError(t, err)
			require.NotEmpty(t, vmReceipt.Incarnation)
			require.Empty(t, state.intent.VMFloatingIPGrants, "planning must not mutate admitted state")
			state.intent = target
			target, netReceipt, err := state.networkFloatingIPTrafficGrantIntent(netGrant, "", "")
			require.NoError(t, err)
			require.NotEmpty(t, netReceipt.Incarnation)
			state.intent = target
			require.Empty(t, target.FloatingIPs[0].OwnerVMID, "policy must not assign the dormant resource")
			_, err = target.compile()
			require.NoError(t, err)
			_, retry, err := state.vmFloatingIPTrafficGrantIntent(vmGrant, "", "")
			require.NoError(t, err)
			require.Equal(t, vmReceipt.Incarnation, retry.Incarnation)
			require.Error(t, func() error {
				_, err := state.removeOwnedVMFloatingIPTrafficGrantIntent(vmGrant.ID, "", "stale")
				return err
			}())
			require.Error(t, func() error {
				_, err := state.removeOwnedNetworkFloatingIPTrafficGrantIntent(netGrant.ID, "", "stale")
				return err
			}())
			target, err = state.removeOwnedVMFloatingIPTrafficGrantIntent(vmGrant.ID, "", vmReceipt.Incarnation)
			require.NoError(t, err)
			require.Empty(t, target.VMFloatingIPGrants)
			state.intent = target
			target, err = state.removeOwnedNetworkFloatingIPTrafficGrantIntent(netGrant.ID, "", netReceipt.Incarnation)
			require.NoError(t, err)
			require.Empty(t, target.NetworkFloatingIPGrants)
			state.intent = target
			_, replacement, err := state.vmFloatingIPTrafficGrantIntent(vmGrant, "", "")
			require.NoError(t, err)
			require.NotEqual(t, vmReceipt.Incarnation, replacement.Incarnation)
		})
	}
}

func TestFloatingIPTrafficPolicyMutationRefusesForeignDestinationAndWrongSourceLock(t *testing.T) {
	for name, change := range map[string]func(*mutationLeaseState){
		"foreign resource":              func(s *mutationLeaseState) { s.intent.FloatingIPs[0].OwnerUID++ },
		"unknown resource":              func(s *mutationLeaseState) { s.intent.FloatingIPs = nil },
		"foreign destination network":   func(s *mutationLeaseState) { s.intent.Networks[0].OwnerUID++ },
		"wrong retained source network": func(s *mutationLeaseState) { s.binding.NetworkID = s.intent.Networks[1].NetworkID },
	} {
		t.Run(name, func(t *testing.T) {
			intent := floatingPolicyIntent()
			vmGrant, netGrant := intent.VMFloatingIPGrants[0], intent.NetworkFloatingIPGrants[0]
			state := &mutationLeaseState{binding: intent.Endpoints[0], intent: intent}
			change(state)
			_, _, err := state.vmFloatingIPTrafficGrantIntent(vmGrant, "", "")
			require.Error(t, err)
			_, _, err = state.networkFloatingIPTrafficGrantIntent(netGrant, "", "")
			require.Error(t, err)
		})
	}
	var lease *MutationLease
	_, err := lease.EnsureVMFloatingIPTrafficGrant(t.Context(), VMFloatingIPTrafficGrant{}, "", "")
	require.Error(t, err)
	lease = &MutationLease{state: &mutationLeaseState{released: true}}
	require.Error(t, lease.RemoveOwnedNetworkFloatingIPTrafficGrant(t.Context(), "", "", ""))
}
