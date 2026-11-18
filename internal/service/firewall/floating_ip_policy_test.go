package firewall

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// floatingPolicyIntent retains four owned endpoints and two resources so a destination expansion
// accidentally following the owner's primary or other alias cannot hide behind a single-address fixture.
func floatingPolicyIntent() Intent {
	intent := secondaryDurableIntent()
	for index, address := range []string{"10.0.0.3", "10.0.0.4", "10.1.0.2"} {
		endpoint := intent.Endpoints[0]
		endpoint.VMID = strings.Repeat(string(rune('2'+index)), 32)
		endpoint.IPv4 = netip.MustParseAddr(address)
		endpoint.MAC[5] = byte(index + 2)
		endpoint.HostVeth.Name = []string{"mvmb0", "mvmc0", "mvmx0"}[index]
		endpoint.HostVeth.IfIndex += index + 1
		if index == 2 {
			endpoint.NetworkID = strings.Repeat("c", 64)
			endpoint.Prefix = netip.MustParsePrefix("10.1.0.0/24")
			endpoint.Gateway = netip.MustParseAddr("10.1.0.1")
			endpoint.ManagedBridge.Name = "mvmbr1"
			endpoint.ManagedBridge.IfIndex = 21
			intent.ActiveNetworks = append(intent.ActiveNetworks, endpoint.NetworkID)
			intent.Networks = append(intent.Networks, NetworkIntent{
				OwnerUID: endpoint.OwnerUID, NetworkID: endpoint.NetworkID, HostBridge: "mvmbr1",
				Subnet: endpoint.Prefix, HostIPv4: endpoint.Gateway,
			})
		}
		intent.Endpoints = append(intent.Endpoints, endpoint)
		intent.Reservations = append(intent.Reservations, endpointReservation(endpoint))
	}
	other := intent.FloatingIPs[0]
	other.ID = strings.Repeat("5", 32)
	other.IPv4 = netip.MustParseAddr("10.0.0.101")
	intent.FloatingIPs = append(intent.FloatingIPs, other)
	vm := VMFloatingIPTrafficGrant{
		Incarnation: "11111111-1111-4111-8111-111111111111",
		SourceVMID:  intent.Endpoints[1].VMID, DestinationFloatingIPID: intent.FloatingIPs[0].ID,
		Protocol: TCP, Ports: PortInterval{First: 443, Last: 443},
	}
	vm.ID = vmFloatingIPGrantDigest(vm)
	network := NetworkFloatingIPTrafficGrant{
		Incarnation: vm.Incarnation, SourceNetworkID: intent.Networks[0].NetworkID,
		DestinationFloatingIPID: intent.FloatingIPs[0].ID, Protocol: UDP, Ports: PortInterval{First: 8000, Last: 8010},
	}
	network.ID = networkFloatingIPGrantDigest(network)
	intent.VMFloatingIPGrants = []VMFloatingIPTrafficGrant{vm}
	intent.NetworkFloatingIPGrants = []NetworkFloatingIPTrafficGrant{network}
	return intent
}

func TestFloatingIPPolicyCompilesOnlyExactDestinationInBothNetworkPaths(t *testing.T) {
	for _, cross := range []bool{false, true} {
		t.Run(map[bool]string{false: "same network", true: "cross network"}[cross], func(t *testing.T) {
			intent := floatingPolicyIntent()
			sourceAddress := []byte{10, 0, 0, 3}
			sourceBridge := "mvmbr0"
			if cross {
				intent.VMFloatingIPGrants[0].SourceVMID = intent.Endpoints[3].VMID
				intent.VMFloatingIPGrants[0].ID = vmFloatingIPGrantDigest(intent.VMFloatingIPGrants[0])
				intent.NetworkFloatingIPGrants[0].SourceNetworkID = intent.Networks[1].NetworkID
				intent.NetworkFloatingIPGrants[0].ID = networkFloatingIPGrantDigest(intent.NetworkFloatingIPGrants[0])
				sourceAddress = []byte{10, 1, 0, 2}
				sourceBridge = "mvmbr1"
			}
			compiled, err := intent.compile()
			require.NoError(t, err)
			require.NoError(t, validateKernelApplicationTarget(compiled))
			vmID := "inet.forward.allow.vm-floating-ip." + intent.VMFloatingIPGrants[0].ID
			networkID := "inet.forward.allow.network-floating-ip." + intent.NetworkFloatingIPGrants[0].ID
			vm := requireCompiledRule(t, compiled, vmID)
			network := requireCompiledRule(t, compiled, networkID)
			assertFloatingPolicyExpressions(t, vm.Exprs, true, false, sourceBridge, "mvmbr0", sourceAddress)
			assertFloatingPolicyExpressions(t, network.Exprs, true, true, sourceBridge, "mvmbr0", nil)
			if !cross {
				vm = requireCompiledRule(t, compiled, "bridge.allow.vm-floating-ip."+intent.VMFloatingIPGrants[0].ID)
				network = requireCompiledRule(
					t,
					compiled,
					"bridge.allow.network-floating-ip."+intent.NetworkFloatingIPGrants[0].ID,
				)
				assertFloatingPolicyExpressions(t, vm.Exprs, false, false, "mvmb0", "mvma0", sourceAddress)
				assertFloatingPolicyExpressions(t, network.Exprs, false, true, "", "mvma0", nil)
			}
			for _, rule := range compiled.bridge.rules {
				if cross {
					assert.False(t, strings.Contains(rule.identity, "allow.vm-floating-ip.") ||
						strings.Contains(rule.identity, "allow.network-floating-ip."))
				}
			}
			for _, rule := range append(slices.Clone(compiled.bridge.rules), compiled.inet.rules...) {
				if strings.Contains(rule.identity, "-floating-ip.") {
					assert.NotContains(t, expressionData(rule.rule.Exprs), []byte{10, 0, 0, 2})
					assert.NotContains(t, expressionData(rule.rule.Exprs), []byte{10, 0, 0, 101})
				}
			}
		})
	}
}

// assertFloatingPolicyExpressions is an independent wire oracle: fixed nft register/offset grammar,
// exact .100 destination, explicit TCP/443 or UDP/8000..8010, and a terminal accept.
func assertFloatingPolicyExpressions(
	t *testing.T, got []expr.Any, inet, networkSource bool, input, output string, sourceAddress []byte,
) {
	t.Helper()
	want := []expr.Any{}
	if !networkSource || inet {
		want = append(want,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: expectedInterfaceName(input)})
	}
	want = append(want, &expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: expectedInterfaceName(output)})
	if inet {
		want = append(want, &expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{2}})
	} else {
		want = append(want, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 12, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{8, 0}})
	}
	want = append(want, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4})
	proto := byte(6)
	if networkSource {
		prefix := []byte{10, 0, 0, 0}
		if input == "mvmbr1" {
			prefix = []byte{10, 1, 0, 0}
		}
		want = append(want, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
			Mask: []byte{255, 255, 255, 0}, Xor: []byte{0, 0, 0, 0}},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: prefix})
		proto = 17
	} else {
		want = append(want, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: sourceAddress})
	}
	want = append(
		want,
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{10, 0, 0, 100}},
		&expr.Meta{
			Key:      expr.MetaKeyL4PROTO,
			Register: 1,
		},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
	)
	if networkSource {
		want = append(want, &expr.Range{Op: expr.CmpOpEq, Register: 1,
			FromData: []byte{0x1f, 0x40}, ToData: []byte{0x1f, 0x4a}})
	} else {
		want = append(want, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{1, 0xbb}})
	}
	want = append(want, &expr.Verdict{Kind: expr.VerdictAccept})
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("resource policy expressions (-want +got):\n%s", diff)
	}
}

func TestFloatingIPPolicyMovementRetainsIdentityAndChangesOnlyDestinationTopology(t *testing.T) {
	intent := floatingPolicyIntent()
	before, err := intent.compile()
	require.NoError(t, err)
	policyID := intent.VMFloatingIPGrants[0].ID
	intent.FloatingIPs[0].OwnerVMID = intent.Endpoints[2].VMID
	intent.FloatingIPs[0].Generation++
	intent.FloatingIPs[0].OwnerLaunchGeneration++
	after, err := intent.compile()
	require.NoError(t, err)
	assert.Equal(t, policyID, vmFloatingIPGrantDigest(intent.VMFloatingIPGrants[0]))
	assert.NotEqual(t, before.id, after.id)
	rule := requireCompiledRule(t, after, "bridge.allow.vm-floating-ip."+policyID)
	assertFloatingPolicyExpressions(t, rule.Exprs, false, false, "mvmb0", "mvmc0", []byte{10, 0, 0, 3})
	requireCompiledRule(t, after, "bridge.floating.destination."+intent.FloatingIPs[0].ID)
}

func TestFloatingIPPolicyDormancyRetainsIntentWithoutDeliveryRules(t *testing.T) {
	for _, state := range []string{"unassociated", "owner stopped", "network inactive"} {
		t.Run(state, func(t *testing.T) {
			intent := floatingPolicyIntent()
			switch state {
			case "unassociated":
				intent.FloatingIPs[0].OwnerVMID = ""
				intent.FloatingIPs[0].OwnerLaunchGeneration = 0
				intent.FloatingIPs[0].LeaseDuration = 0
			case "owner stopped":
				intent.Endpoints = intent.Endpoints[1:]
			case "network inactive":
				intent.ActiveNetworks = intent.ActiveNetworks[1:]
				intent.Endpoints = intent.Endpoints[3:]
			}
			compiled, err := intent.compile()
			require.NoError(t, err)
			for _, rule := range append(slices.Clone(compiled.bridge.rules), compiled.inet.rules...) {
				assert.False(t, strings.Contains(rule.identity, "-floating-ip."), rule.identity)
			}
			assert.Equal(t, 1, len(intent.VMFloatingIPGrants))
			assert.Equal(t, 1, len(intent.NetworkFloatingIPGrants))
		})
	}
}

func TestFloatingIPPolicyRefusesCorruptDurableInventoryEvenWhenDormant(t *testing.T) {
	cases := map[string]func(*Intent){
		"missing resource": func(i *Intent) { i.FloatingIPs = nil },
		"missing VM source": func(i *Intent) {
			i.VMFloatingIPGrants[0].SourceVMID = strings.Repeat("9", 32)
			i.VMFloatingIPGrants[0].ID = vmFloatingIPGrantDigest(i.VMFloatingIPGrants[0])
		},
		"missing network source": func(i *Intent) {
			i.NetworkFloatingIPGrants[0].SourceNetworkID = strings.Repeat("9", 64)
			i.NetworkFloatingIPGrants[0].ID = networkFloatingIPGrantDigest(i.NetworkFloatingIPGrants[0])
		},
		"forged digest":   func(i *Intent) { i.VMFloatingIPGrants[0].ID = strings.Repeat("a", 64) },
		"bad incarnation": func(i *Intent) { i.VMFloatingIPGrants[0].Incarnation = "bad" },
		"bad claim":       func(i *Intent) { i.NetworkFloatingIPGrants[0].ClaimID = "bad" },
		"zero port": func(i *Intent) {
			i.VMFloatingIPGrants[0].Ports.First = 0
			i.VMFloatingIPGrants[0].ID = vmFloatingIPGrantDigest(i.VMFloatingIPGrants[0])
		},
		"reversed ports": func(i *Intent) {
			i.NetworkFloatingIPGrants[0].Ports.Last = 1
			i.NetworkFloatingIPGrants[0].ID = networkFloatingIPGrantDigest(i.NetworkFloatingIPGrants[0])
		},
		"unsupported protocol": func(i *Intent) {
			i.VMFloatingIPGrants[0].Protocol = Protocol(112)
			i.VMFloatingIPGrants[0].ID = vmFloatingIPGrantDigest(i.VMFloatingIPGrants[0])
		},
		"duplicate": func(i *Intent) { i.VMFloatingIPGrants = append(i.VMFloatingIPGrants, i.VMFloatingIPGrants[0]) },
		"collision with old family": func(i *Intent) {
			i.VMGrants = []VMTrafficGrant{{
				ID: i.VMFloatingIPGrants[0].ID, SourceVMID: i.Reservations[1].VMID,
				DestinationVMID: i.Reservations[0].VMID, Protocol: TCP, Ports: PortInterval{First: 443, Last: 443}}}
		},
		"foreign source owner": func(i *Intent) {
			i.Networks[1].OwnerUID = 2000
			i.Reservations[3].OwnerUID = 2000
			i.VMFloatingIPGrants[0].SourceVMID = i.Reservations[3].VMID
			i.VMFloatingIPGrants[0].ID = vmFloatingIPGrantDigest(i.VMFloatingIPGrants[0])
		},
		"foreign network owner": func(i *Intent) {
			i.Networks[1].OwnerUID = 2000
			i.Reservations[3].OwnerUID = 2000
			i.NetworkFloatingIPGrants[0].SourceNetworkID = i.Networks[1].NetworkID
			i.NetworkFloatingIPGrants[0].ID = networkFloatingIPGrantDigest(i.NetworkFloatingIPGrants[0])
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			intent := floatingPolicyIntent()
			intent.Endpoints = nil
			mutate(&intent)
			_, err := intent.compile()
			assert.Error(t, err)
		})
	}
}

func TestFloatingIPPolicyCloneOrderingAndApplicationSeal(t *testing.T) {
	intent := floatingPolicyIntent()
	second := intent.VMFloatingIPGrants[0]
	second.SourceVMID = intent.Endpoints[3].VMID
	second.ID = vmFloatingIPGrantDigest(second)
	intent.VMFloatingIPGrants = append(intent.VMFloatingIPGrants, second)
	secondNetwork := intent.NetworkFloatingIPGrants[0]
	secondNetwork.SourceNetworkID = intent.Networks[1].NetworkID
	secondNetwork.ID = networkFloatingIPGrantDigest(secondNetwork)
	intent.NetworkFloatingIPGrants = append(intent.NetworkFloatingIPGrants, secondNetwork)
	before, err := intent.compile()
	require.NoError(t, err)
	clone := cloneIntent(intent)
	slices.Reverse(clone.VMFloatingIPGrants)
	slices.Reverse(clone.NetworkFloatingIPGrants)
	slices.Reverse(clone.FloatingIPs)
	slices.Reverse(clone.Endpoints)
	after, err := clone.compile()
	require.NoError(t, err)
	assert.Equal(t, before.id, after.id)
	if diff := cmp.Diff(before.applicationSeal, after.applicationSeal); diff != "" {
		t.Errorf("application seal ordering (-want +got):\n%s", diff)
	}
	clone.VMFloatingIPGrants[0].Ports = PortInterval{First: 444, Last: 444}
	clone.NetworkFloatingIPGrants[0].Protocol = TCP
	assert.Equal(t, uint16(443), intent.VMFloatingIPGrants[1].Ports.First)
	assert.Equal(t, UDP, intent.NetworkFloatingIPGrants[1].Protocol)
	rule := requireCompiledRule(t, after, "inet.forward.allow.vm-floating-ip."+second.ID)
	rule.Exprs[len(rule.Exprs)-1] = &expr.Verdict{Kind: expr.VerdictDrop}
	assert.Error(t, validateKernelApplicationTarget(after))
	without := cloneIntent(intent)
	without.VMFloatingIPGrants = nil
	without.NetworkFloatingIPGrants = nil
	old, err := without.compile()
	require.NoError(t, err)
	assert.NotEqual(t, before.id, old.id)
}
