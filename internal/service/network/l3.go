package network

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

const guestMACDerivationDomain = "mvmctl:guest-mac:v1"

type ipv4Prefix struct {
	value netip.Prefix
}

type ipv4Address struct {
	value netip.Addr
}

type guestMAC [6]byte

type addressScope uint8

const (
	addressScopeUniverse addressScope = iota + 1
	addressScopeHost
)

type routeTable uint8

const (
	routeTableMain routeTable = iota + 1
	routeTableLocal
)

type routeKind uint8

const (
	routeKindUnicast routeKind = iota + 1
	routeKindLocal
)

type routeProtocol uint8

const (
	routeProtocolKernel routeProtocol = iota + 1
	routeProtocolBoot
)

type routeScope uint8

const (
	routeScopeUniverse routeScope = iota + 1
	routeScopeLink
	routeScopeHost
)

type l3Cardinality uint8

const (
	zero l3Cardinality = iota
	exactlyOne
)

type addressExpectation struct {
	prefix netip.Prefix
	scope  addressScope
}

type routeExpectation struct {
	destination netip.Prefix
	source      netip.Addr
	gateway     netip.Addr
	hasGateway  bool
	table       routeTable
	kind        routeKind
	protocol    routeProtocol
	scope       routeScope
	metric      uint32
	hasMetric   bool
}

type managedNetworkL3Input struct {
	prefix  netip.Prefix
	gateway *netip.Addr
}

type managedNetworkL3 struct {
	prefix               ipv4Prefix
	gateway              ipv4Address
	bridgeAddress        addressExpectation
	bridgeConnectedRoute routeExpectation
}

type endpointL3Input struct {
	guestAddress netip.Addr
	requestedMAC *string
	reservations guestMACReservationSnapshot
}

type endpointL3 struct {
	network        managedNetworkL3
	guestAddress   ipv4Address
	mac            guestMAC
	address        addressExpectation
	connectedRoute routeExpectation
	defaultRoute   routeExpectation
}

type guestMACReservationSnapshot struct {
	state *guestMACReservationState
}

type guestMACReservationState struct {
	values []guestMAC
}

type loopbackL3Expectation struct {
	address        addressExpectation
	localRoute     routeExpectation
	connectedRoute routeExpectation
}

type fabricL3Expectation struct {
	ipv4Addresses l3Cardinality
	ipv4Routes    l3Cardinality
}

var (
	canonicalLoopbackL3 = loopbackL3Expectation{
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
	canonicalFabricL3 = fabricL3Expectation{ipv4Addresses: zero, ipv4Routes: zero}
	privateIPv4Ranges = [...]netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
)

func admitManagedNetworkL3(input managedNetworkL3Input) (managedNetworkL3, error) {
	if !input.prefix.IsValid() || !input.prefix.Addr().Is4() {
		return managedNetworkL3{}, topologyValidationError("managed network prefix must be valid IPv4")
	}
	if input.prefix != input.prefix.Masked() {
		return managedNetworkL3{}, topologyValidationError("managed network prefix must be canonically masked")
	}
	if input.prefix.Bits() > 30 {
		return managedNetworkL3{}, topologyValidationError(
			"managed network prefix must provide distinct usable addresses",
		)
	}
	if !prefixWithinPrivateIPv4(input.prefix) {
		return managedNetworkL3{}, topologyValidationError(
			"managed network prefix must be wholly contained in RFC1918 space",
		)
	}

	gateway := input.prefix.Addr().Next()
	if input.gateway != nil {
		gateway = *input.gateway
	}
	if !usableIPv4Address(input.prefix, gateway) {
		return managedNetworkL3{}, topologyValidationError(
			"managed network gateway must be a usable address in its prefix",
		)
	}

	return managedNetworkL3{
		prefix:  ipv4Prefix{value: input.prefix},
		gateway: ipv4Address{value: gateway},
		bridgeAddress: addressExpectation{
			prefix: netip.PrefixFrom(gateway, input.prefix.Bits()), scope: addressScopeUniverse,
		},
		bridgeConnectedRoute: routeExpectation{
			destination: input.prefix,
			source:      gateway,
			table:       routeTableMain,
			kind:        routeKindUnicast,
			protocol:    routeProtocolKernel,
			scope:       routeScopeLink,
		},
	}, nil
}

func admitEndpointL3(key topologyKey, network managedNetworkL3, input endpointL3Input) (endpointL3, error) {
	if err := validateTopologyKey(key); err != nil {
		return endpointL3{}, err
	}
	if err := validateManagedNetworkL3(network); err != nil {
		return endpointL3{}, err
	}
	if input.reservations.state == nil {
		return endpointL3{}, topologyValidationError("guest MAC reservation snapshot is required")
	}
	if !usableIPv4Address(network.prefix.value, input.guestAddress) {
		return endpointL3{}, topologyValidationError(
			"guest address must be a usable address in the managed network prefix",
		)
	}
	if input.guestAddress == network.gateway.value {
		return endpointL3{}, topologyValidationError("guest address must differ from the managed network gateway")
	}

	mac := deriveDefaultGuestMAC(key.ownerUID, key.vmID)
	if input.requestedMAC != nil {
		parsed, err := parseCanonicalGuestMAC(*input.requestedMAC)
		if err != nil {
			return endpointL3{}, err
		}
		mac = parsed
	}
	if input.reservations.contains(mac) {
		return endpointL3{}, topologyValidationError("selected guest MAC is already reserved")
	}

	return endpointL3{
		network:      network,
		guestAddress: ipv4Address{value: input.guestAddress},
		mac:          mac,
		address: addressExpectation{
			prefix: netip.PrefixFrom(input.guestAddress, network.prefix.value.Bits()),
			scope:  addressScopeUniverse,
		},
		connectedRoute: routeExpectation{
			destination: network.prefix.value,
			source:      input.guestAddress,
			table:       routeTableMain,
			kind:        routeKindUnicast,
			protocol:    routeProtocolKernel,
			scope:       routeScopeLink,
		},
		defaultRoute: routeExpectation{
			destination: netip.MustParsePrefix("0.0.0.0/0"),
			gateway:     network.gateway.value,
			hasGateway:  true,
			table:       routeTableMain,
			kind:        routeKindUnicast,
			protocol:    routeProtocolBoot,
			scope:       routeScopeUniverse,
		},
	}, nil
}

func validateManagedNetworkL3(network managedNetworkL3) error {
	gateway := network.gateway.value
	want, err := admitManagedNetworkL3(managedNetworkL3Input{
		prefix: network.prefix.value, gateway: &gateway,
	})
	if err != nil {
		return err
	}
	if want != network {
		return topologyValidationError("managed network L3 authority is not canonical")
	}
	return nil
}

func newGuestMACReservationSnapshot(values []guestMAC) guestMACReservationSnapshot {
	return guestMACReservationSnapshot{state: &guestMACReservationState{
		values: append([]guestMAC(nil), values...),
	}}
}

func (snapshot guestMACReservationSnapshot) contains(candidate guestMAC) bool {
	if snapshot.state == nil {
		return false
	}
	for _, reserved := range snapshot.state.values {
		if reserved == candidate {
			return true
		}
	}
	return false
}

func deriveDefaultGuestMAC(ownerUID OwnerUID, vmID VMID) guestMAC {
	preimage := guestMACDerivationDomain + "\x00" +
		strconv.FormatUint(uint64(ownerUID), 10) + "\x00" + string(vmID)
	digest := sha256.Sum256([]byte(preimage))
	return guestMAC{0x02, 0xfc, digest[0], digest[1], digest[2], digest[3]}
}

func parseCanonicalGuestMAC(value string) (guestMAC, error) {
	if !canonicalMACText(value) {
		return guestMAC{}, topologyValidationError(
			"explicit guest MAC must be canonical lower-case EUI-48",
		)
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, ":", ""))
	if err != nil || len(decoded) != 6 {
		return guestMAC{}, topologyValidationError(
			"explicit guest MAC must be canonical lower-case EUI-48",
		)
	}
	mac := guestMAC(decoded)
	if mac[0]&1 != 0 || mac == (guestMAC{}) {
		return guestMAC{}, topologyValidationError("explicit guest MAC must be unicast and non-zero")
	}
	return mac, nil
}

func canonicalMACText(value string) bool {
	if len(value) != 17 {
		return false
	}
	for index, character := range value {
		if index%3 == 2 {
			if character != ':' {
				return false
			}
			continue
		}
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (mac guestMAC) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

func prefixWithinPrivateIPv4(prefix netip.Prefix) bool {
	last := lastIPv4Address(prefix)
	for _, private := range privateIPv4Ranges {
		if private.Contains(prefix.Addr()) && private.Contains(last) {
			return true
		}
	}
	return false
}

func usableIPv4Address(prefix netip.Prefix, address netip.Addr) bool {
	return address.IsValid() && address.Is4() && prefix.Contains(address) &&
		address != prefix.Addr() && address != lastIPv4Address(prefix)
}

func lastIPv4Address(prefix netip.Prefix) netip.Addr {
	bytes := prefix.Addr().As4()
	value := uint64(binary.BigEndian.Uint32(bytes[:]))
	hostBits := 32 - prefix.Bits()
	last := value | (uint64(1)<<hostBits - 1)
	var result [4]byte
	binary.BigEndian.PutUint32(result[:], uint32(last))
	return netip.AddrFrom4(result)
}

func validateRouteExpectation(expectation routeExpectation) error {
	if !expectation.destination.IsValid() || !expectation.destination.Addr().Is4() ||
		expectation.destination != expectation.destination.Masked() || !expectation.source.Is4() ||
		expectation.table == 0 || expectation.kind == 0 || expectation.protocol == 0 || expectation.scope == 0 {
		return errors.New("route expectation is incomplete")
	}
	if expectation.hasGateway != expectation.gateway.IsValid() ||
		(expectation.hasGateway && !expectation.gateway.Is4()) || expectation.hasMetric {
		return errors.New("route expectation has unsupported gateway or metric state")
	}
	return nil
}
