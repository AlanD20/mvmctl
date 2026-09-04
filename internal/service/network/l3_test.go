package network

import (
	"crypto/sha256"
	"net/netip"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestAdmitManagedNetworkL3CanonicalizesPrivatePrefixAndGateway(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prefix  string
		gateway *netip.Addr
		want    managedNetworkL3
	}{
		"ten slash eight defaults first usable": {
			prefix: "10.0.0.0/8",
			want:   contractManagedNetworkL3("10.0.0.0/8", "10.0.0.1"),
		},
		"172 private boundary accepts explicit gateway": {
			prefix:  "172.31.255.252/30",
			gateway: addressPointer(netip.MustParseAddr("172.31.255.254")),
			want:    contractManagedNetworkL3("172.31.255.252/30", "172.31.255.254"),
		},
		"192 private range accepts slash thirty": {
			prefix: "192.168.255.252/30",
			want:   contractManagedNetworkL3("192.168.255.252/30", "192.168.255.253"),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := admitManagedNetworkL3(managedNetworkL3Input{
				prefix: netip.MustParsePrefix(test.prefix), gateway: test.gateway,
			})

			require.NoError(t, err)
			if diff := cmp.Diff(test.want, got,
				cmp.AllowUnexported(
					managedNetworkL3{}, ipv4Prefix{}, ipv4Address{}, addressExpectation{}, routeExpectation{},
				),
				cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
			); diff != "" {
				t.Errorf("managed network L3 mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: A masked private prefix and usable gateway are root-side admission boundaries. Accepting a host-bit
// prefix, public space, /31-/32, or a network/broadcast gateway would make later exact route inspection ambiguous.
func TestAdmitManagedNetworkL3RejectsNonCanonicalOrUnusableInput(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input       managedNetworkL3Input
		wantMessage string
	}{
		"invalid prefix": {
			input: managedNetworkL3Input{}, wantMessage: "managed network prefix must be valid IPv4",
		},
		"valid IPv6 prefix": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("fd00::/64")},
			wantMessage: "managed network prefix must be valid IPv4",
		},
		"host bits set": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("10.4.0.1/24")},
			wantMessage: "managed network prefix must be canonically masked",
		},
		"public prefix": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("203.0.113.0/24")},
			wantMessage: "managed network prefix must be wholly contained in RFC1918 space",
		},
		"crosses private boundary": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("172.0.0.0/8")},
			wantMessage: "managed network prefix must be wholly contained in RFC1918 space",
		},
		"ten slash seven crosses private boundary": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("10.0.0.0/7")},
			wantMessage: "managed network prefix must be wholly contained in RFC1918 space",
		},
		"slash thirty one": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("10.0.0.0/31")},
			wantMessage: "managed network prefix must provide distinct usable addresses",
		},
		"slash thirty two": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("255.255.255.255/32")},
			wantMessage: "managed network prefix must provide distinct usable addresses",
		},
		"gateway is network": {
			input: managedNetworkL3Input{
				prefix:  netip.MustParsePrefix("10.0.0.0/24"),
				gateway: addressPointer(netip.MustParseAddr("10.0.0.0")),
			},
			wantMessage: "managed network gateway must be a usable address in its prefix",
		},
		"gateway is broadcast": {
			input: managedNetworkL3Input{
				prefix:  netip.MustParsePrefix("10.0.0.0/24"),
				gateway: addressPointer(netip.MustParseAddr("10.0.0.255")),
			},
			wantMessage: "managed network gateway must be a usable address in its prefix",
		},
		"gateway outside prefix": {
			input: managedNetworkL3Input{
				prefix:  netip.MustParsePrefix("10.0.0.0/24"),
				gateway: addressPointer(netip.MustParseAddr("10.0.1.1")),
			},
			wantMessage: "managed network gateway must be a usable address in its prefix",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := admitManagedNetworkL3(test.input)

			require.Error(t, err)
			assert.Equal(t, test.wantMessage, err.Error())
		})
	}
}

// Rationale: The closed managed-network value is authority, not a bag of individually trusted fields. Every field
// must match one canonical derivation before a privileged effect consumes it.
func TestValidateManagedNetworkL3RejectsEveryTamperedAuthorityField(t *testing.T) {
	t.Parallel()

	canonical := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	require.NoError(t, validateManagedNetworkL3(canonical))
	tests := map[string]func(*managedNetworkL3){
		"prefix": func(value *managedNetworkL3) {
			value.prefix.value = netip.MustParsePrefix("10.44.0.0/25")
		},
		"gateway": func(value *managedNetworkL3) {
			value.gateway.value = netip.MustParseAddr("10.44.0.2")
		},
		"bridge address prefix": func(value *managedNetworkL3) {
			value.bridgeAddress.prefix = netip.MustParsePrefix("10.44.0.2/24")
		},
		"bridge address scope": func(value *managedNetworkL3) {
			value.bridgeAddress.scope = addressScopeHost
		},
		"route destination": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.destination = netip.MustParsePrefix("10.44.0.0/25")
		},
		"route source": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.source = netip.MustParseAddr("10.44.0.2")
		},
		"route source presence": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.hasSource = false
		},
		"route gateway": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.gateway = netip.MustParseAddr("10.44.0.2")
		},
		"route gateway presence": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.hasGateway = true
		},
		"route table": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.table = routeTableLocal
		},
		"route kind": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.kind = routeKindLocal
		},
		"route protocol": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.protocol = routeProtocolBoot
		},
		"route scope": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.scope = routeScopeHost
		},
		"route metric": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.metric = 1
		},
		"route metric presence": func(value *managedNetworkL3) {
			value.bridgeConnectedRoute.hasMetric = true
		},
	}

	for name, tamper := range tests {
		t.Run(name, func(t *testing.T) {
			value := canonical
			tamper(&value)

			err := validateManagedNetworkL3(value)

			require.Error(t, err)
			assert.Equal(t, "managed network L3 authority is not canonical", err.Error())
		})
	}
}

func TestDeriveEndpointL3ProducesExactGuestContractAndDefaultMAC(t *testing.T) {
	t.Parallel()

	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	network := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	got, err := deriveEndpointL3(key, network, endpointL3Input{
		guestAddress: netip.MustParseAddr("10.44.0.27"),
	})

	require.NoError(t, err)
	want := endpointL3{
		key:          key,
		network:      network,
		guestAddress: ipv4Address{value: netip.MustParseAddr("10.44.0.27")},
		mac:          contractDefaultGuestMAC(testOwnerUID, testVMID),
		macSelection: guestMACDerived,
		address: addressExpectation{
			prefix: netip.MustParsePrefix("10.44.0.27/24"), scope: addressScopeUniverse,
		},
		connectedRoute: routeExpectation{
			destination: netip.MustParsePrefix("10.44.0.0/24"),
			source:      netip.MustParseAddr("10.44.0.27"),
			hasSource:   true,
			table:       routeTableMain,
			kind:        routeKindUnicast,
			protocol:    routeProtocolKernel,
			scope:       routeScopeLink,
		},
		defaultRoute: routeExpectation{
			destination: netip.MustParsePrefix("0.0.0.0/0"),
			gateway:     netip.MustParseAddr("10.44.0.1"),
			hasGateway:  true,
			table:       routeTableMain,
			kind:        routeKindUnicast,
			protocol:    routeProtocolBoot,
			scope:       routeScopeUniverse,
		},
	}
	if diff := cmp.Diff(want, got,
		cmp.AllowUnexported(
			endpointL3{}, topologyKey{}, managedNetworkL3{}, ipv4Prefix{}, ipv4Address{},
			addressExpectation{}, routeExpectation{},
		),
		cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
	); diff != "" {
		t.Errorf("endpoint L3 mismatch (-want +got):\n%s", diff)
	}
	assertGuestMACEqual(t, guestMAC{0x02, 0xfc, 0x1c, 0x97, 0x39, 0xee}, got.mac)
	require.NoError(t, validateEndpointL3(key, network, got))
}

func TestDeriveEndpointL3UsesBothUsableSlashThirtyAssignments(t *testing.T) {
	t.Parallel()

	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	tests := map[string]struct {
		gateway         string
		guest           string
		explicitGateway bool
	}{
		"default gateway and sole remaining guest": {gateway: "10.44.0.1", guest: "10.44.0.2"},
		"explicit reversed gateway and guest": {
			gateway: "10.44.0.2", guest: "10.44.0.1", explicitGateway: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gateway := netip.MustParseAddr(test.gateway)
			input := managedNetworkL3Input{prefix: netip.MustParsePrefix("10.44.0.0/30")}
			if test.explicitGateway {
				input.gateway = &gateway
			}
			network, err := admitManagedNetworkL3(input)
			require.NoError(t, err)

			got, err := deriveEndpointL3(key, network, endpointL3Input{
				guestAddress: netip.MustParseAddr(test.guest),
			})

			require.NoError(t, err)
			if diff := cmp.Diff(
				netip.MustParsePrefix(test.guest+"/30"),
				got.address.prefix,
				cmpopts.EquateComparable(netip.Prefix{}),
			); diff != "" {
				t.Errorf("guest prefix mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(gateway, got.defaultRoute.gateway, cmpopts.EquateComparable(netip.Addr{})); diff != "" {
				t.Errorf("default-route gateway mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: Pure endpoint derivation validates a canonical candidate but does not claim global MAC uniqueness.
// Complete-inventory collision proof belongs under firewall serialization and the later opaque endpoint lease.
func TestDeriveEndpointL3RejectsUnusableGuestAddress(t *testing.T) {
	t.Parallel()

	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	network := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	tests := map[string]struct {
		input       endpointL3Input
		wantMessage string
	}{
		"guest is gateway": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.1"),
			},
			wantMessage: "guest address must differ from the managed network gateway",
		},
		"guest is network": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.0"),
			},
			wantMessage: "guest address must be a usable address in the managed network prefix",
		},
		"guest is broadcast": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.255"),
			},
			wantMessage: "guest address must be a usable address in the managed network prefix",
		},
		"guest outside prefix": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.45.0.2"),
			},
			wantMessage: "guest address must be a usable address in the managed network prefix",
		},
		"guest is IPv6": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("fd00::2"),
			},
			wantMessage: "guest address must be a usable address in the managed network prefix",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := deriveEndpointL3(key, network, test.input)

			require.Error(t, err)
			assert.Equal(t, test.wantMessage, err.Error())
		})
	}
}

// Rationale: net.ParseMAC accepts several textual dialects and multicast values. The privileged boundary requires
// one canonical lower-case EUI-48 spelling so equivalent caller strings cannot become distinct persisted identities.
func TestDeriveEndpointL3RequiresCanonicalUnicastExplicitMAC(t *testing.T) {
	t.Parallel()

	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	network := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	tests := map[string]struct {
		value       string
		want        guestMAC
		wantMessage string
	}{
		"canonical local unicast":  {value: "02:11:22:33:44:55", want: guestMAC{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}},
		"canonical global unicast": {value: "00:11:22:33:44:55", want: guestMAC{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}},
		"upper case": {
			value:       "02:11:22:33:44:AA",
			wantMessage: "explicit guest MAC must be canonical lower-case EUI-48",
		},
		"hyphenated": {
			value:       "02-11-22-33-44-55",
			wantMessage: "explicit guest MAC must be canonical lower-case EUI-48",
		},
		"short": {
			value:       "02:11:22:33:44",
			wantMessage: "explicit guest MAC must be canonical lower-case EUI-48",
		},
		"multicast": {
			value:       "01:11:22:33:44:55",
			wantMessage: "explicit guest MAC must be unicast and non-zero",
		},
		"zero": {
			value:       "00:00:00:00:00:00",
			wantMessage: "explicit guest MAC must be unicast and non-zero",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := deriveEndpointL3(key, network, endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.2"),
				requestedMAC: &test.value,
			})
			if test.wantMessage != "" {
				require.Error(t, err)
				assert.Equal(t, test.wantMessage, err.Error())
				return
			}
			require.NoError(t, err)
			assertGuestMACEqual(t, test.want, got.mac)
			assert.Equal(t, guestMACExplicit, got.macSelection)
			require.NoError(t, validateEndpointL3(key, network, got))
		})
	}
}

// Rationale: Endpoint L3 authority is retained for one immutable topology. Validation at each consumer must reject
// owner, VM, or network rebinding and any changed derived endpoint field.
func TestValidateEndpointL3RejectsTopologyRebindingAndTampering(t *testing.T) {
	t.Parallel()

	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	network := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	endpoint, err := deriveEndpointL3(key, network, endpointL3Input{
		guestAddress: netip.MustParseAddr("10.44.0.2"),
	})
	require.NoError(t, err)
	otherVM := mustTopologyKey(t, testOwnerUID, "cccccccccccccccccccccccccccccccc", testNetworkID)
	otherNetwork := mustTopologyKey(
		t,
		testOwnerUID,
		testVMID,
		"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	)
	tests := map[string]struct {
		key     topologyKey
		network managedNetworkL3
		mutate  func(*endpointL3)
	}{
		"owner rebound at use": {key: topologyKey{
			ownerUID: OwnerUID(testOwnerUID + 1), vmID: VMID(testVMID), networkID: NetworkID(testNetworkID),
		}},
		"VM rebound at use":      {key: otherVM},
		"network rebound at use": {key: otherNetwork},
		"retained owner changed": {key: key, mutate: func(value *endpointL3) {
			value.key.ownerUID++
		}},
		"retained VM changed": {key: key, mutate: func(value *endpointL3) {
			value.key = otherVM
		}},
		"retained network changed": {key: key, mutate: func(value *endpointL3) {
			value.key = otherNetwork
		}},
		"managed network changed": {
			key:     key,
			network: contractManagedNetworkL3("10.45.0.0/24", "10.45.0.1"),
		},
		"guest address changed": {key: key, mutate: func(value *endpointL3) {
			value.guestAddress.value = netip.MustParseAddr("10.44.0.3")
		}},
		"derived MAC changed": {key: key, mutate: func(value *endpointL3) {
			value.mac = guestMAC{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}
		}},
		"MAC selection changed": {key: key, mutate: func(value *endpointL3) {
			value.macSelection = 0
		}},
		"guest address expectation changed": {key: key, mutate: func(value *endpointL3) {
			value.address.prefix = netip.MustParsePrefix("10.44.0.3/24")
		}},
		"connected route changed": {key: key, mutate: func(value *endpointL3) {
			value.connectedRoute.source = netip.MustParseAddr("10.44.0.3")
		}},
		"default route changed": {key: key, mutate: func(value *endpointL3) {
			value.defaultRoute.gateway = netip.MustParseAddr("10.44.0.2")
		}},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			value := endpoint
			if test.mutate != nil {
				test.mutate(&value)
			}
			useNetwork := test.network
			if useNetwork == (managedNetworkL3{}) {
				useNetwork = network
			}

			err := validateEndpointL3(test.key, useNetwork, value)

			require.Error(t, err)
		})
	}
}

// Rationale: Preferred-source absence is state, not an invalid zero value. Kernel-derived bridge, guest-connected,
// and local routes carry an exact source; the canonical metric-free guest default route deliberately carries none.
func TestEveryDerivedRouteDeclaresAndValidatesCanonicalSourcePresence(t *testing.T) {
	t.Parallel()

	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	network := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	endpoint, err := deriveEndpointL3(key, network, endpointL3Input{
		guestAddress: netip.MustParseAddr("10.44.0.2"),
	})
	require.NoError(t, err)
	tests := map[string]struct {
		route         routeExpectation
		wantHasSource bool
	}{
		"managed bridge connected": {route: network.bridgeConnectedRoute, wantHasSource: true},
		"guest connected":          {route: endpoint.connectedRoute, wantHasSource: true},
		"guest default":            {route: endpoint.defaultRoute, wantHasSource: false},
		"loopback local":           {route: canonicalLoopbackL3.localRoute, wantHasSource: true},
		"loopback connected":       {route: canonicalLoopbackL3.connectedRoute, wantHasSource: true},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.wantHasSource, test.route.hasSource)
			assert.Equal(t, test.wantHasSource, test.route.source.IsValid())
			require.NoError(t, validateRouteExpectation(test.route))
		})
	}
}

func TestValidateRouteExpectationRejectsSourcePresenceMismatch(t *testing.T) {
	t.Parallel()

	network := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	connected := network.bridgeConnectedRoute
	defaultRoute := routeExpectation{
		destination: netip.MustParsePrefix("0.0.0.0/0"),
		gateway:     netip.MustParseAddr("10.44.0.1"),
		hasGateway:  true,
		table:       routeTableMain,
		kind:        routeKindUnicast,
		protocol:    routeProtocolBoot,
		scope:       routeScopeUniverse,
	}
	require.NoError(t, validateRouteExpectation(connected))
	require.NoError(t, validateRouteExpectation(defaultRoute))
	tests := map[string]routeExpectation{
		"source value without presence": func() routeExpectation {
			value := connected
			value.hasSource = false
			return value
		}(),
		"presence without source value": func() routeExpectation {
			value := connected
			value.source = netip.Addr{}
			return value
		}(),
		"kernel route without source": func() routeExpectation {
			value := connected
			value.source = netip.Addr{}
			value.hasSource = false
			return value
		}(),
		"boot default route with source": func() routeExpectation {
			value := defaultRoute
			value.source = netip.MustParseAddr("10.44.0.2")
			value.hasSource = true
			return value
		}(),
	}

	for name, route := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRouteExpectation(route)

			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
			assert.Equal(t, errs.ClassValidation, domainErr.Class)
		})
	}
}

func TestFixedLoopbackAndFabricL3Expectations(t *testing.T) {
	t.Parallel()

	wantLoopback := loopbackL3Expectation{
		address: addressExpectation{
			prefix: netip.MustParsePrefix("127.0.0.1/8"), scope: addressScopeHost,
		},
		localRoute: routeExpectation{
			destination: netip.MustParsePrefix("127.0.0.1/32"),
			source:      netip.MustParseAddr("127.0.0.1"),
			hasSource:   true,
			table:       routeTableLocal,
			kind:        routeKindLocal,
			protocol:    routeProtocolKernel,
			scope:       routeScopeHost,
		},
		connectedRoute: routeExpectation{
			destination: netip.MustParsePrefix("127.0.0.0/8"),
			source:      netip.MustParseAddr("127.0.0.1"),
			hasSource:   true,
			table:       routeTableLocal,
			kind:        routeKindLocal,
			protocol:    routeProtocolKernel,
			scope:       routeScopeHost,
		},
	}
	if diff := cmp.Diff(wantLoopback, canonicalLoopbackL3,
		cmp.AllowUnexported(loopbackL3Expectation{}, addressExpectation{}, routeExpectation{}),
		cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
	); diff != "" {
		t.Errorf("loopback L3 contract mismatch (-want +got):\n%s", diff)
	}
	wantFabric := fabricL3Expectation{ipv4Addresses: zero, ipv4Routes: zero}
	if diff := cmp.Diff(wantFabric, canonicalFabricL3, cmp.AllowUnexported(fabricL3Expectation{})); diff != "" {
		t.Errorf("fabric L3 contract mismatch (-want +got):\n%s", diff)
	}
}

func contractManagedNetworkL3(prefix, gateway string) managedNetworkL3 {
	prefixValue := netip.MustParsePrefix(prefix)
	gatewayValue := netip.MustParseAddr(gateway)
	return managedNetworkL3{
		prefix:  ipv4Prefix{value: prefixValue},
		gateway: ipv4Address{value: gatewayValue},
		bridgeAddress: addressExpectation{
			prefix: netip.PrefixFrom(gatewayValue, prefixValue.Bits()), scope: addressScopeUniverse,
		},
		bridgeConnectedRoute: routeExpectation{
			destination: prefixValue,
			source:      gatewayValue,
			hasSource:   true,
			table:       routeTableMain,
			kind:        routeKindUnicast,
			protocol:    routeProtocolKernel,
			scope:       routeScopeLink,
		},
	}
}

func contractDefaultGuestMAC(ownerUID uint32, vmID string) guestMAC {
	preimage := "mvmctl:guest-mac:v1\x00" + strconv.FormatUint(uint64(ownerUID), 10) + "\x00" + vmID
	digest := sha256.Sum256([]byte(preimage))
	return guestMAC{0x02, 0xfc, digest[0], digest[1], digest[2], digest[3]}
}

func addressPointer(value netip.Addr) *netip.Addr { return &value }

func assertGuestMACEqual(t *testing.T, want, got guestMAC) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("guest MAC mismatch (-want +got):\n%s", diff)
	}
}
