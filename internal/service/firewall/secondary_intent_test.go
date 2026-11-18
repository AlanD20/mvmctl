package firewall

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secondaryDurableIntent() Intent {
	endpoint := EndpointBinding{
		OwnerUID: 1000, VMID: strings.Repeat("1", 32), NetworkID: strings.Repeat("b", 64),
		BootID: "11111111-1111-4111-8111-111111111111", HostNamespace: NamespaceIdentity{Device: 1, Inode: 2},
		Prefix: netip.MustParsePrefix("10.0.0.0/24"), Gateway: netip.MustParseAddr("10.0.0.1"),
		IPv4: netip.MustParseAddr("10.0.0.2"), MAC: [6]byte{2, 0, 0, 0, 0, 1},
		ManagedBridge: LinkIdentity{Name: "mvmbr0", IfIndex: 11, NamespaceDevice: 1, NamespaceInode: 2},
		HostVeth:      LinkIdentity{Name: "mvma0", IfIndex: 12, NamespaceDevice: 1, NamespaceInode: 2},
	}
	return Intent{
		ActiveNetworks: []string{endpoint.NetworkID}, Endpoints: []EndpointBinding{endpoint},
		Reservations: []EndpointReservation{endpointReservation(endpoint)},
		Networks: []NetworkIntent{{OwnerUID: 1000, NetworkID: endpoint.NetworkID, HostBridge: "mvmbr0",
			Subnet: endpoint.Prefix, HostIPv4: endpoint.Gateway}},
		FloatingIPs: []FloatingIPBinding{{OwnerUID: 1000, ID: strings.Repeat("4", 32), NetworkID: endpoint.NetworkID,
			IPv4: netip.MustParseAddr("10.0.0.100"), Generation: 7, OwnerVMID: endpoint.VMID,
			OwnerLaunchGeneration: 9, LeaseDuration: 5 * time.Second}},
	}
}

func TestSecondaryDurableIntentSuppressesStoppedOwnerAuthority(t *testing.T) {
	for _, state := range []string{"active", "stopped", "inactive network", "unassociated"} {
		t.Run(state, func(t *testing.T) {
			intent := secondaryDurableIntent()
			switch state {
			case "stopped":
				intent.Endpoints = nil
			case "inactive network":
				intent.Endpoints = nil
				intent.ActiveNetworks = nil
			case "unassociated":
				intent.FloatingIPs[0].OwnerVMID = ""
				intent.FloatingIPs[0].OwnerLaunchGeneration = 0
				intent.FloatingIPs[0].LeaseDuration = 0
			}
			compiled, err := intent.compile()
			require.NoError(t, err)
			if state == "inactive network" {
				assert.True(t, compiled.empty)
				assert.Empty(t, compiled.floatingIPs)
				return
			}
			require.Len(t, compiled.floatingIPs, 1)
			assert.Equal(t, uint64(7), compiled.floatingIPs[0].generation)
			if state == "active" {
				assert.Equal(t, intent.Endpoints[0].VMID, compiled.floatingIPs[0].ownerVMID)
				assert.Equal(t, secondaryLiveKey(), compiled.bridge.secondaryOwners[intent.FloatingIPs[0].ID])
			} else {
				assert.Empty(t, compiled.floatingIPs[0].ownerVMID)
				assert.Zero(t, compiled.floatingIPs[0].ownerLaunchGeneration)
				assert.Zero(t, compiled.floatingIPs[0].leaseDuration)
				assert.Empty(t, compiled.bridge.secondaryOwners)
			}
		})
	}
}

func TestSecondaryDurableIntentRefusesCorruptAuthorityEvenWithoutAnActiveOwner(t *testing.T) {
	for name, mutate := range map[string]func(*Intent){
		"foreign operator":          func(i *Intent) { i.FloatingIPs[0].OwnerUID = 1001 },
		"unknown network":           func(i *Intent) { i.FloatingIPs[0].NetworkID = "missing" },
		"unknown owner":             func(i *Intent) { i.FloatingIPs[0].OwnerVMID = strings.Repeat("9", 32) },
		"zero generation":           func(i *Intent) { i.FloatingIPs[0].Generation = 0 },
		"stopped primary collision": func(i *Intent) { i.FloatingIPs[0].IPv4 = netip.MustParseAddr("10.0.0.2") },
		"gateway collision":         func(i *Intent) { i.FloatingIPs[0].IPv4 = netip.MustParseAddr("10.0.0.1") },
		"broadcast":                 func(i *Intent) { i.FloatingIPs[0].IPv4 = netip.MustParseAddr("10.0.0.255") },
		"zero lease":                func(i *Intent) { i.FloatingIPs[0].LeaseDuration = 0 },
		"fractional lease":          func(i *Intent) { i.FloatingIPs[0].LeaseDuration += time.Millisecond },
		"missing incarnation":       func(i *Intent) { i.FloatingIPs[0].OwnerLaunchGeneration = 0 },
		"owner outside resource network": func(i *Intent) {
			i.Networks = append(i.Networks, NetworkIntent{OwnerUID: 1000, NetworkID: strings.Repeat("c", 64), HostBridge: "mvmbr1",
				Subnet: netip.MustParsePrefix("10.1.0.0/24"), HostIPv4: netip.MustParseAddr("10.1.0.1")})
			i.FloatingIPs[0].NetworkID = strings.Repeat("c", 64)
			i.FloatingIPs[0].IPv4 = netip.MustParseAddr("10.1.0.100")
		},
		"duplicate identity": func(i *Intent) {
			copy := i.FloatingIPs[0]
			copy.IPv4 = netip.MustParseAddr("10.0.0.101")
			i.FloatingIPs = append(i.FloatingIPs, copy)
		},
		"duplicate address": func(i *Intent) {
			copy := i.FloatingIPs[0]
			copy.ID = strings.Repeat("5", 32)
			i.FloatingIPs = append(i.FloatingIPs, copy)
		},
	} {
		t.Run(name, func(t *testing.T) {
			intent := secondaryDurableIntent()
			intent.Endpoints = nil
			mutate(&intent)
			_, err := intent.compile()
			requireKernelObservationRejected(t, err)
		})
	}
}
