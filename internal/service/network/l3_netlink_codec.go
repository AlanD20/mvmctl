package network

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const routeL3MessageMax = 64 * 1024

type routeAddressObservation struct {
	ifIndex int
	prefix  netip.Prefix
	scope   uint8
	flags   uint32
}

type routeIPv4Observation struct {
	destination netip.Prefix
	source      netip.Addr
	hasSource   bool
	gateway     netip.Addr
	ifIndex     int
	table       uint32
	protocol    uint8
	scope       uint8
	kind        uint8
	metric      uint32
	hasGateway  bool
	hasMetric   bool
}

func decodeRouteAddressMessage(message netlink.Message) (routeAddressObservation, error) {
	if message.Header.Type != netlink.HeaderType(unix.RTM_NEWADDR) {
		return routeAddressObservation{}, fmt.Errorf(
			"unexpected route-address message type %d",
			message.Header.Type,
		)
	}
	if len(message.Data) < unix.SizeofIfAddrmsg || len(message.Data) > routeL3MessageMax {
		return routeAddressObservation{}, errors.New("route-address message has invalid size")
	}
	if message.Data[0] != unix.AF_INET {
		return routeAddressObservation{}, errors.New("route-address message is not IPv4")
	}
	prefixLength := int(message.Data[1])
	if prefixLength < 0 || prefixLength > 32 {
		return routeAddressObservation{}, errors.New("route-address prefix length is invalid")
	}
	indexValue := binary.NativeEndian.Uint32(message.Data[4:8])
	if indexValue == 0 || uint64(indexValue) > uint64(math.MaxInt) {
		return routeAddressObservation{}, errors.New("route-address ifindex is invalid")
	}
	observation := routeAddressObservation{
		ifIndex: int(indexValue),
		scope:   message.Data[3],
		flags:   uint32(message.Data[2]),
	}
	decoder, err := netlink.NewAttributeDecoder(message.Data[unix.SizeofIfAddrmsg:])
	if err != nil {
		return routeAddressObservation{}, fmt.Errorf("decode route-address attributes: %w", err)
	}
	seen := make(map[uint16]struct{}, 3)
	var local, peer netip.Addr
	for decoder.Next() {
		typeID := decoder.Type()
		switch typeID {
		case unix.IFA_LOCAL, unix.IFA_ADDRESS, unix.IFA_FLAGS:
			if _, exists := seen[typeID]; exists {
				return routeAddressObservation{}, fmt.Errorf("duplicate route-address attribute %d", typeID)
			}
			seen[typeID] = struct{}{}
		}
		switch typeID {
		case unix.IFA_LOCAL:
			local, err = decodeRouteIPv4(decoder.Bytes())
		case unix.IFA_ADDRESS:
			peer, err = decodeRouteIPv4(decoder.Bytes())
		case unix.IFA_FLAGS:
			observation.flags = decoder.Uint32()
		}
		if err != nil {
			return routeAddressObservation{}, fmt.Errorf("decode route-address IPv4 attribute: %w", err)
		}
	}
	if err := decoder.Err(); err != nil {
		return routeAddressObservation{}, fmt.Errorf("decode route-address attributes: %w", err)
	}
	if !local.IsValid() || !peer.IsValid() {
		return routeAddressObservation{}, errors.New("route-address message lacks local or address authority")
	}
	if local != peer {
		return routeAddressObservation{}, errors.New("route-address local and address authorities differ")
	}
	observation.prefix = netip.PrefixFrom(local, prefixLength)
	return observation, nil
}

func decodeRouteIPv4Message(message netlink.Message) (routeIPv4Observation, error) {
	if message.Header.Type != netlink.HeaderType(unix.RTM_NEWROUTE) {
		return routeIPv4Observation{}, fmt.Errorf("unexpected IPv4 route message type %d", message.Header.Type)
	}
	if len(message.Data) < unix.SizeofRtMsg || len(message.Data) > routeL3MessageMax {
		return routeIPv4Observation{}, errors.New("IPv4 route message has invalid size")
	}
	if message.Data[0] != unix.AF_INET || message.Data[2] != 0 || message.Data[3] != 0 {
		return routeIPv4Observation{}, errors.New("route message is not canonical IPv4 unicast authority")
	}
	destinationLength := int(message.Data[1])
	if destinationLength < 0 || destinationLength > 32 {
		return routeIPv4Observation{}, errors.New("IPv4 route destination length is invalid")
	}
	observation := routeIPv4Observation{
		destination: netip.MustParsePrefix("0.0.0.0/0"),
		table:       uint32(message.Data[4]),
		protocol:    message.Data[5],
		scope:       message.Data[6],
		kind:        message.Data[7],
	}
	// LINKDOWN reports carrier state, not route authority. An otherwise exact connected route
	// can carry this flag while its newly raised bridge has no attached VM yet.
	if binary.NativeEndian.Uint32(message.Data[8:12])&^uint32(unix.RTNH_F_LINKDOWN) != 0 {
		return routeIPv4Observation{}, errors.New("IPv4 route flags are unsupported")
	}
	decoder, err := netlink.NewAttributeDecoder(message.Data[unix.SizeofRtMsg:])
	if err != nil {
		return routeIPv4Observation{}, fmt.Errorf("decode IPv4 route attributes: %w", err)
	}
	seen := make(map[uint16]struct{}, 6)
	var destination netip.Addr
	for decoder.Next() {
		typeID := decoder.Type()
		switch typeID {
		case unix.RTA_DST, unix.RTA_PREFSRC, unix.RTA_GATEWAY, unix.RTA_OIF, unix.RTA_PRIORITY, unix.RTA_TABLE:
			if _, exists := seen[typeID]; exists {
				return routeIPv4Observation{}, fmt.Errorf("duplicate IPv4 route attribute %d", typeID)
			}
			seen[typeID] = struct{}{}
		case unix.RTA_MULTIPATH:
			return routeIPv4Observation{}, errors.New("multipath IPv4 route authority is unsupported")
		case unix.RTA_CACHEINFO, unix.RTA_PAD:
			// Cache timing and alignment are observation metadata, not route authority.
		default:
			return routeIPv4Observation{}, fmt.Errorf("unsupported IPv4 route authority attribute %d", typeID)
		}
		switch typeID {
		case unix.RTA_DST:
			destination, err = decodeRouteIPv4(decoder.Bytes())
		case unix.RTA_PREFSRC:
			observation.source, err = decodeRouteIPv4(decoder.Bytes())
			observation.hasSource = err == nil
		case unix.RTA_GATEWAY:
			observation.gateway, err = decodeRouteIPv4(decoder.Bytes())
			observation.hasGateway = err == nil
		case unix.RTA_OIF:
			value := decoder.Uint32()
			if value == 0 || uint64(value) > uint64(math.MaxInt) {
				return routeIPv4Observation{}, errors.New("IPv4 route output ifindex is invalid")
			}
			observation.ifIndex = int(value)
		case unix.RTA_PRIORITY:
			observation.metric = decoder.Uint32()
			observation.hasMetric = true
		case unix.RTA_TABLE:
			observation.table = decoder.Uint32()
		}
		if err != nil {
			return routeIPv4Observation{}, fmt.Errorf("decode IPv4 route address attribute: %w", err)
		}
	}
	if err := decoder.Err(); err != nil {
		return routeIPv4Observation{}, fmt.Errorf("decode IPv4 route attributes: %w", err)
	}
	if observation.ifIndex <= 0 {
		return routeIPv4Observation{}, errors.New("IPv4 route lacks one output interface")
	}
	if destinationLength != 0 {
		if !destination.IsValid() {
			return routeIPv4Observation{}, errors.New("IPv4 route destination is missing")
		}
		observation.destination = netip.PrefixFrom(destination, destinationLength)
		if observation.destination != observation.destination.Masked() {
			return routeIPv4Observation{}, errors.New("IPv4 route destination is not canonically masked")
		}
	} else if destination.IsValid() {
		return routeIPv4Observation{}, errors.New("default IPv4 route has an unexpected destination attribute")
	}
	return observation, nil
}

func marshalIfAddrMessage(ifIndex int, prefixLength int, scope uint8) ([]byte, error) {
	if ifIndex <= 0 || uint64(ifIndex) > uint64(math.MaxUint32) {
		return nil, errors.New("address ifindex is outside the rtnetlink range")
	}
	if prefixLength < 0 || prefixLength > 32 {
		return nil, errors.New("address prefix length is outside the IPv4 range")
	}
	data := make([]byte, unix.SizeofIfAddrmsg)
	data[0] = unix.AF_INET
	data[1] = byte(prefixLength)
	data[3] = scope
	binary.NativeEndian.PutUint32(data[4:8], uint32(ifIndex))
	return data, nil
}

func marshalRouteMessage(destinationLength int, table, protocol, scope, kind uint8) ([]byte, error) {
	if destinationLength < 0 || destinationLength > 32 {
		return nil, errors.New("route destination length is outside the IPv4 range")
	}
	data := make([]byte, unix.SizeofRtMsg)
	data[0] = unix.AF_INET
	data[1] = byte(destinationLength)
	data[4] = table
	data[5] = protocol
	data[6] = scope
	data[7] = kind
	return data, nil
}

func decodeRouteIPv4(data []byte) (netip.Addr, error) {
	if len(data) != 4 {
		return netip.Addr{}, errors.New("IPv4 netlink attribute must contain four bytes")
	}
	var value [4]byte
	copy(value[:], data)
	return netip.AddrFrom4(value), nil
}
