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

type routeNeighborObservation struct {
	ifIndex int
	address netip.Addr
	state   uint16
	flags   uint8
	kind    uint8
	mac     [6]byte
	hasMAC  bool
}

func marshalIPv4NeighborMessage(ifIndex int, address netip.Addr) ([]byte, error) {
	if ifIndex <= 0 || uint64(ifIndex) > math.MaxUint32 || !address.Is4() {
		return nil, errors.New("neighbor requires an exact IPv4 address and positive interface index")
	}
	data := make([]byte, unix.SizeofNdMsg)
	data[0] = unix.AF_INET
	binary.NativeEndian.PutUint32(data[4:8], uint32(ifIndex))
	return appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.Bytes(unix.NDA_DST, address.AsSlice())
		return nil
	})
}

func decodeIPv4NeighborMessage(message netlink.Message) (routeNeighborObservation, error) {
	if message.Header.Type != netlink.HeaderType(unix.RTM_NEWNEIGH) ||
		len(message.Data) < unix.SizeofNdMsg || len(message.Data) > routeL3MessageMax {
		return routeNeighborObservation{}, errors.New("neighbor reply has invalid type or size")
	}
	data := message.Data
	if data[0] != unix.AF_INET || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return routeNeighborObservation{}, errors.New("neighbor reply is not canonical IPv4")
	}
	index := binary.NativeEndian.Uint32(data[4:8])
	if index == 0 || uint64(index) > uint64(math.MaxInt) {
		return routeNeighborObservation{}, errors.New("neighbor reply has invalid interface index")
	}
	observation := routeNeighborObservation{
		ifIndex: int(index), state: binary.NativeEndian.Uint16(data[8:10]),
		flags: data[10], kind: data[11],
	}
	decoder, err := netlink.NewAttributeDecoder(data[unix.SizeofNdMsg:])
	if err != nil {
		return routeNeighborObservation{}, fmt.Errorf("decode neighbor attributes: %w", err)
	}
	seen := make(map[uint16]struct{}, 2)
	for decoder.Next() {
		attribute := decoder.Type()
		if _, duplicate := seen[attribute]; duplicate {
			return routeNeighborObservation{}, fmt.Errorf("duplicate neighbor attribute %d", attribute)
		}
		seen[attribute] = struct{}{}
		switch attribute {
		case unix.NDA_DST:
			observation.address, err = decodeRouteIPv4(decoder.Bytes())
		case unix.NDA_LLADDR:
			value := decoder.Bytes()
			if len(value) != len(observation.mac) {
				return routeNeighborObservation{}, errors.New("neighbor hardware address is not Ethernet")
			}
			copy(observation.mac[:], value)
			observation.hasMAC = true
		case unix.NDA_CACHEINFO:
			if len(decoder.Bytes()) != 16 {
				return routeNeighborObservation{}, errors.New("neighbor cache metadata has invalid size")
			}
		case unix.NDA_PROBES:
			if len(decoder.Bytes()) != 4 {
				return routeNeighborObservation{}, errors.New("neighbor probe metadata has invalid size")
			}
		default:
			return routeNeighborObservation{}, fmt.Errorf("unsupported neighbor attribute %d", attribute)
		}
		if err != nil {
			return routeNeighborObservation{}, fmt.Errorf("decode neighbor address: %w", err)
		}
	}
	if err := decoder.Err(); err != nil {
		return routeNeighborObservation{}, fmt.Errorf("decode neighbor attributes: %w", err)
	}
	if !observation.address.Is4() {
		return routeNeighborObservation{}, errors.New("neighbor reply lacks an IPv4 destination")
	}
	return observation, nil
}

func admitDynamicIPv4Neighbor(observation routeNeighborObservation, ifIndex int, address netip.Addr) error {
	if observation.ifIndex != ifIndex || observation.address != address {
		return errors.New("neighbor reply does not match the admitted bridge and guest address")
	}
	if observation.flags != 0 || observation.kind != unix.RTN_UNICAST {
		return errors.New("neighbor has unsupported flags or route kind")
	}
	switch observation.state {
	case unix.NUD_INCOMPLETE, unix.NUD_REACHABLE, unix.NUD_STALE,
		unix.NUD_DELAY, unix.NUD_PROBE, unix.NUD_FAILED:
		if observation.state != unix.NUD_INCOMPLETE && observation.state != unix.NUD_FAILED && !observation.hasMAC {
			return errors.New("resolved neighbor lacks an Ethernet address")
		}
		return nil
	default:
		return errors.New("neighbor is not a dynamic IPv4 entry")
	}
}
