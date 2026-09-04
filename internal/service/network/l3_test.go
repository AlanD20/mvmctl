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
		"slash thirty one": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("10.0.0.0/31")},
			wantMessage: "managed network prefix must provide distinct usable addresses",
		},
		"slash thirty two": {
			input:       managedNetworkL3Input{prefix: netip.MustParsePrefix("10.0.0.1/32")},
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

func TestAdmitEndpointL3DerivesExactGuestContractAndDefaultMAC(t *testing.T) {
	t.Parallel()

	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	network := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	reservations := newGuestMACReservationSnapshot(nil)

	got, err := admitEndpointL3(key, network, endpointL3Input{
		guestAddress: netip.MustParseAddr("10.44.0.27"),
		reservations: reservations,
	})

	require.NoError(t, err)
	want := endpointL3{
		network:      network,
		guestAddress: ipv4Address{value: netip.MustParseAddr("10.44.0.27")},
		mac:          contractDefaultGuestMAC(testOwnerUID, testVMID),
		address: addressExpectation{
			prefix: netip.MustParsePrefix("10.44.0.27/24"), scope: addressScopeUniverse,
		},
		connectedRoute: routeExpectation{
			destination: netip.MustParsePrefix("10.44.0.0/24"),
			source:      netip.MustParseAddr("10.44.0.27"),
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
			endpointL3{}, managedNetworkL3{}, ipv4Prefix{}, ipv4Address{},
			addressExpectation{}, routeExpectation{},
		),
		cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
	); diff != "" {
		t.Errorf("endpoint L3 mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, "02:fc:1c:97:39:ee", got.mac.String())
}

// Rationale: Endpoint admission must fail before privileged composition when guest identity conflicts with the
// gateway, falls outside usable host space, or its selected MAC is already reserved. Default MAC collisions are not
// silently salted because restore and anti-spoof identity must remain deterministic.
func TestAdmitEndpointL3RejectsAddressAndMACConflicts(t *testing.T) {
	t.Parallel()

	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	network := contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1")
	defaultMAC := contractDefaultGuestMAC(testOwnerUID, testVMID)
	explicit := "02:11:22:33:44:55"
	tests := map[string]struct {
		input       endpointL3Input
		wantMessage string
	}{
		"missing reservation snapshot": {
			input:       endpointL3Input{guestAddress: netip.MustParseAddr("10.44.0.2")},
			wantMessage: "guest MAC reservation snapshot is required",
		},
		"guest is gateway": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.1"),
				reservations: newGuestMACReservationSnapshot(nil),
			},
			wantMessage: "guest address must differ from the managed network gateway",
		},
		"guest is network": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.0"),
				reservations: newGuestMACReservationSnapshot(nil),
			},
			wantMessage: "guest address must be a usable address in the managed network prefix",
		},
		"guest is broadcast": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.255"),
				reservations: newGuestMACReservationSnapshot(nil),
			},
			wantMessage: "guest address must be a usable address in the managed network prefix",
		},
		"guest outside prefix": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.45.0.2"),
				reservations: newGuestMACReservationSnapshot(nil),
			},
			wantMessage: "guest address must be a usable address in the managed network prefix",
		},
		"default MAC collision": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.2"),
				reservations: newGuestMACReservationSnapshot([]guestMAC{defaultMAC}),
			},
			wantMessage: "selected guest MAC is already reserved",
		},
		"explicit MAC collision": {
			input: endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.2"),
				requestedMAC: &explicit,
				reservations: newGuestMACReservationSnapshot([]guestMAC{{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}}),
			},
			wantMessage: "selected guest MAC is already reserved",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := admitEndpointL3(key, network, test.input)

			require.Error(t, err)
			assert.Equal(t, test.wantMessage, err.Error())
		})
	}
}

// Rationale: net.ParseMAC accepts several textual dialects and multicast values. The privileged boundary requires
// one canonical lower-case EUI-48 spelling so equivalent caller strings cannot become distinct persisted identities.
func TestAdmitEndpointL3RequiresCanonicalUnicastExplicitMAC(t *testing.T) {
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
			got, err := admitEndpointL3(key, network, endpointL3Input{
				guestAddress: netip.MustParseAddr("10.44.0.2"),
				requestedMAC: &test.value,
				reservations: newGuestMACReservationSnapshot(nil),
			})
			if test.wantMessage != "" {
				require.Error(t, err)
				assert.Equal(t, test.wantMessage, err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got.mac)
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
			table:       routeTableLocal,
			kind:        routeKindLocal,
			protocol:    routeProtocolKernel,
			scope:       routeScopeHost,
		},
		connectedRoute: routeExpectation{
			destination: netip.MustParsePrefix("127.0.0.0/8"),
			source:      netip.MustParseAddr("127.0.0.1"),
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
	assert.Equal(t, fabricL3Expectation{ipv4Addresses: zero, ipv4Routes: zero}, canonicalFabricL3)
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
