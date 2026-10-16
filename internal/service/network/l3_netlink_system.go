package network

import (
	"context"
	"errors"
	"fmt"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

func (adapter *directRouteNetlink) listIPv4Addresses(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
) ([]routeAddressObservation, error) {
	data, err := marshalIfAddrMessage(ifIndex, 0, unix.RT_SCOPE_UNIVERSE)
	if err != nil {
		return nil, err
	}
	replies, err := adapter.execute(ctx, namespaceFD, netlink.Message{
		Header: netlink.Header{
			Type: netlink.HeaderType(unix.RTM_GETADDR), Flags: netlink.Request | netlink.Dump,
		},
		Data: data,
	})
	if err != nil {
		return nil, err
	}
	observations := make([]routeAddressObservation, 0, len(replies))
	for index, reply := range replies {
		observation, decodeErr := decodeRouteAddressMessage(reply)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode route-address dump reply %d: %w", index, decodeErr)
		}
		if observation.ifIndex == ifIndex {
			observations = append(observations, observation)
		}
	}
	return observations, nil
}

func (adapter *directRouteNetlink) listIPv4Routes(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
) ([]routeIPv4Observation, error) {
	if ifIndex <= 0 {
		return nil, errors.New("route output ifindex must be positive")
	}
	data, err := marshalRouteMessage(
		0,
		unix.RT_TABLE_UNSPEC,
		unix.RTPROT_UNSPEC,
		unix.RT_SCOPE_UNIVERSE,
		unix.RTN_UNSPEC,
	)
	if err != nil {
		return nil, err
	}
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.Uint32(unix.RTA_OIF, uint32(ifIndex))
		return nil
	})
	if err != nil {
		return nil, err
	}
	replies, err := adapter.execute(ctx, namespaceFD, netlink.Message{
		Header: netlink.Header{
			Type: netlink.HeaderType(unix.RTM_GETROUTE), Flags: netlink.Request | netlink.Dump,
		},
		Data: data,
	})
	if err != nil {
		return nil, err
	}
	observations := make([]routeIPv4Observation, 0, len(replies))
	for index, reply := range replies {
		observation, decodeErr := decodeRouteIPv4Message(reply)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode IPv4 route dump reply %d: %w", index, decodeErr)
		}
		if observation.ifIndex == ifIndex {
			observations = append(observations, observation)
		}
	}
	return observations, nil
}

func (adapter *directRouteNetlink) addIPv4Address(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
	expectation addressExpectation,
) error {
	scope, err := addressScopeToKernel(expectation.scope)
	if err != nil {
		return err
	}
	if !expectation.prefix.IsValid() || !expectation.prefix.Addr().Is4() {
		return errors.New("IPv4 address expectation is invalid")
	}
	data, err := marshalIfAddrMessage(ifIndex, expectation.prefix.Bits(), scope)
	if err != nil {
		return err
	}
	address := expectation.prefix.Addr().AsSlice()
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.Bytes(unix.IFA_LOCAL, address)
		encoder.Bytes(unix.IFA_ADDRESS, address)
		return nil
	})
	if err != nil {
		return err
	}
	return adapter.mutate(
		ctx,
		namespaceFD,
		unix.RTM_NEWADDR,
		netlink.Create|netlink.Excl,
		data,
	)
}

func (adapter *directRouteNetlink) setLinkUp(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
	up bool,
) error {
	if ifIndex <= 0 {
		return errors.New("link-state ifindex must be positive")
	}
	var flags uint32
	if up {
		flags = unix.IFF_UP
	}
	data, err := marshalIfInfoMessage(ifIndex, flags, unix.IFF_UP)
	if err != nil {
		return err
	}
	return adapter.mutate(ctx, namespaceFD, unix.RTM_NEWLINK, 0, data)
}

func addressScopeToKernel(scope addressScope) (uint8, error) {
	switch scope {
	case addressScopeUniverse:
		return unix.RT_SCOPE_UNIVERSE, nil
	case addressScopeHost:
		return unix.RT_SCOPE_HOST, nil
	default:
		return 0, errors.New("address scope is invalid")
	}
}

func routeExpectationToObservation(
	expectation routeExpectation,
	ifIndex int,
) (routeIPv4Observation, error) {
	if err := validateRouteExpectation(expectation); err != nil {
		return routeIPv4Observation{}, err
	}
	table, protocol, scope, kind, err := routeExpectationKernelValues(expectation)
	if err != nil {
		return routeIPv4Observation{}, err
	}
	return routeIPv4Observation{
		destination: expectation.destination,
		source:      expectation.source,
		hasSource:   expectation.hasSource,
		gateway:     expectation.gateway,
		ifIndex:     ifIndex,
		table:       table,
		protocol:    protocol,
		scope:       scope,
		kind:        kind,
		metric:      expectation.metric,
		hasGateway:  expectation.hasGateway,
		hasMetric:   expectation.hasMetric,
	}, nil
}

func routeExpectationKernelValues(
	expectation routeExpectation,
) (table uint32, protocol, scope, kind uint8, returnErr error) {
	switch expectation.table {
	case routeTableMain:
		table = unix.RT_TABLE_MAIN
	case routeTableLocal:
		table = unix.RT_TABLE_LOCAL
	default:
		returnErr = errors.New("route table is invalid")
	}
	switch expectation.protocol {
	case routeProtocolKernel:
		protocol = unix.RTPROT_KERNEL
	case routeProtocolBoot:
		protocol = unix.RTPROT_BOOT
	default:
		returnErr = errors.Join(returnErr, errors.New("route protocol is invalid"))
	}
	switch expectation.scope {
	case routeScopeUniverse:
		scope = unix.RT_SCOPE_UNIVERSE
	case routeScopeLink:
		scope = unix.RT_SCOPE_LINK
	case routeScopeHost:
		scope = unix.RT_SCOPE_HOST
	default:
		returnErr = errors.Join(returnErr, errors.New("route scope is invalid"))
	}
	switch expectation.kind {
	case routeKindUnicast:
		kind = unix.RTN_UNICAST
	case routeKindLocal:
		kind = unix.RTN_LOCAL
	case routeKindBroadcast:
		kind = unix.RTN_BROADCAST
	default:
		returnErr = errors.Join(returnErr, errors.New("route kind is invalid"))
	}
	return table, protocol, scope, kind, returnErr
}

func addressExpectationToObservation(
	expectation addressExpectation,
	ifIndex int,
) (routeAddressObservation, error) {
	scope, err := addressScopeToKernel(expectation.scope)
	if err != nil {
		return routeAddressObservation{}, err
	}
	if ifIndex <= 0 || !expectation.prefix.IsValid() || !expectation.prefix.Addr().Is4() {
		return routeAddressObservation{}, errors.New("IPv4 address expectation is invalid")
	}
	return routeAddressObservation{ifIndex: ifIndex, prefix: expectation.prefix, scope: scope}, nil
}

func exactAddressObservation(
	actual routeAddressObservation,
	want addressExpectation,
	ifIndex int,
) error {
	expected, err := addressExpectationToObservation(want, ifIndex)
	if err != nil {
		return err
	}
	// Kernel flags are observation-only. Address, prefix, scope, and interface are the fixed L3 contract.
	actual.flags = 0
	if actual != expected {
		return errors.New("IPv4 address does not match its exact derived expectation")
	}
	return nil
}
