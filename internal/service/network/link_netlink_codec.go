package network

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	routeLinkMessageMax = 64 * 1024
	vethInfoPeer        = uint16(1)
	iflaTunOwner        = uint16(1)
	iflaTunType         = uint16(3)
	iflaTunPI           = uint16(4)
	iflaTunPersist      = uint16(6)

	managedLinkAliasVersion = "mvmctl:v1"
	linkKindBridge          = "bridge"
	linkKindTAP             = "tun"
	linkKindVeth            = "veth"
)

type routeLinkObservation struct {
	name    linkName
	alias   string
	kind    string
	ifIndex int
	flags   uint32
	master  int
	peer    int

	tunOwner   uint32
	tunType    uint8
	tunPI      uint8
	tunPersist uint8
}

func managedBridgeAlias(name linkName) string {
	return managedLinkAliasVersion + ":bridge:" + name.String()
}

func managedTAPAlias(name linkName, owner OwnerUID) string {
	return fmt.Sprintf("%s:tap:%s:%d", managedLinkAliasVersion, name, owner)
}

func managedVethAlias(role string, name, peer linkName) string {
	return managedLinkAliasVersion + ":" + role + ":" + name.String() + ":" + peer.String()
}

func decodeRouteLinkMessage(message netlink.Message) (routeLinkObservation, error) {
	if message.Header.Type != netlink.HeaderType(unix.RTM_NEWLINK) {
		return routeLinkObservation{}, fmt.Errorf("unexpected route-link message type %d", message.Header.Type)
	}
	if len(message.Data) < unix.SizeofIfInfomsg || len(message.Data) > routeLinkMessageMax {
		return routeLinkObservation{}, errors.New("route-link message has invalid size")
	}
	if message.Data[0] != unix.AF_UNSPEC {
		return routeLinkObservation{}, errors.New("route-link message has unexpected address family")
	}
	index := int(int32(binary.NativeEndian.Uint32(message.Data[4:8])))
	if index <= 0 {
		return routeLinkObservation{}, errors.New("route-link message has invalid ifindex")
	}
	observation := routeLinkObservation{
		ifIndex: index,
		flags:   binary.NativeEndian.Uint32(message.Data[8:12]),
	}
	decoder, err := netlink.NewAttributeDecoder(message.Data[unix.SizeofIfInfomsg:])
	if err != nil {
		return routeLinkObservation{}, fmt.Errorf("decode route-link attributes: %w", err)
	}
	seen := make(map[uint16]struct{}, 5)
	var nameSeen bool
	for decoder.Next() {
		typeID := decoder.Type()
		switch typeID {
		case unix.IFLA_IFNAME, unix.IFLA_IFALIAS, unix.IFLA_MASTER, unix.IFLA_LINK, unix.IFLA_LINKINFO:
			if _, exists := seen[typeID]; exists {
				return routeLinkObservation{}, fmt.Errorf("duplicate route-link attribute %d", typeID)
			}
			seen[typeID] = struct{}{}
		}
		switch typeID {
		case unix.IFLA_IFNAME:
			name, decodeErr := decodeRouteCString(decoder.Bytes(), linuxInterfaceNameMax)
			if decodeErr != nil {
				return routeLinkObservation{}, fmt.Errorf("decode route-link name: %w", decodeErr)
			}
			observation.name = linkName(name)
			nameSeen = true
		case unix.IFLA_IFALIAS:
			alias, decodeErr := decodeRouteCString(decoder.Bytes(), 255)
			if decodeErr != nil {
				return routeLinkObservation{}, fmt.Errorf("decode route-link alias: %w", decodeErr)
			}
			observation.alias = alias
		case unix.IFLA_MASTER:
			value := decoder.Uint32()
			if uint64(value) > uint64(math.MaxInt) {
				return routeLinkObservation{}, errors.New("route-link master exceeds native integer range")
			}
			observation.master = int(value)
		case unix.IFLA_LINK:
			value := decoder.Uint32()
			if uint64(value) > uint64(math.MaxInt) {
				return routeLinkObservation{}, errors.New("route-link peer exceeds native integer range")
			}
			observation.peer = int(value)
		case unix.IFLA_LINKINFO:
			if err := decodeRouteLinkInfo(decoder.Bytes(), &observation); err != nil {
				return routeLinkObservation{}, err
			}
		}
	}
	if err := decoder.Err(); err != nil {
		return routeLinkObservation{}, fmt.Errorf("decode route-link attributes: %w", err)
	}
	if !nameSeen || observation.name == "" {
		return routeLinkObservation{}, errors.New("route-link message lacks an interface name")
	}
	return observation, nil
}

func decodeRouteLinkInfo(data []byte, observation *routeLinkObservation) error {
	decoder, err := netlink.NewAttributeDecoder(data)
	if err != nil {
		return fmt.Errorf("decode route-link linkinfo: %w", err)
	}
	var kindSeen, dataSeen bool
	var kindData []byte
	for decoder.Next() {
		switch decoder.Type() {
		case unix.IFLA_INFO_KIND:
			if kindSeen {
				return errors.New("duplicate route-link kind")
			}
			kind, decodeErr := decodeRouteCString(decoder.Bytes(), 32)
			if decodeErr != nil {
				return fmt.Errorf("decode route-link kind: %w", decodeErr)
			}
			observation.kind = kind
			kindSeen = true
		case unix.IFLA_INFO_DATA:
			if dataSeen {
				return errors.New("duplicate route-link kind data")
			}
			kindData = decoder.Bytes()
			dataSeen = true
		}
	}
	if err := decoder.Err(); err != nil {
		return fmt.Errorf("decode route-link linkinfo: %w", err)
	}
	if !kindSeen {
		return errors.New("route-link kind is missing")
	}
	if observation.kind == linkKindTAP {
		if !dataSeen {
			return errors.New("TAP link data is missing")
		}
		if err := decodeRouteTUNData(kindData, observation); err != nil {
			return err
		}
	}
	return nil
}

func decodeRouteTUNData(data []byte, observation *routeLinkObservation) error {
	decoder, err := netlink.NewAttributeDecoder(data)
	if err != nil {
		return fmt.Errorf("decode TAP link data: %w", err)
	}
	seen := make(map[uint16]struct{}, 4)
	for decoder.Next() {
		typeID := decoder.Type()
		switch typeID {
		case iflaTunOwner, iflaTunType, iflaTunPI, iflaTunPersist:
			if _, exists := seen[typeID]; exists {
				return fmt.Errorf("duplicate TAP link attribute %d", typeID)
			}
			seen[typeID] = struct{}{}
		}
		switch typeID {
		case iflaTunOwner:
			observation.tunOwner = decoder.Uint32()
		case iflaTunType:
			observation.tunType = decoder.Uint8()
		case iflaTunPI:
			observation.tunPI = decoder.Uint8()
		case iflaTunPersist:
			observation.tunPersist = decoder.Uint8()
		}
	}
	if err := decoder.Err(); err != nil {
		return fmt.Errorf("decode TAP link data: %w", err)
	}
	for _, required := range []uint16{iflaTunOwner, iflaTunType, iflaTunPI, iflaTunPersist} {
		if _, exists := seen[required]; !exists {
			return fmt.Errorf("TAP link attribute %d is missing", required)
		}
	}
	return nil
}

func decodeRouteCString(data []byte, maximum int) (string, error) {
	if len(data) < 2 || len(data) > maximum+1 || data[len(data)-1] != 0 {
		return "", errors.New("netlink string has invalid size or terminator")
	}
	value := data[:len(data)-1]
	if strings.IndexByte(string(value), 0) >= 0 {
		return "", errors.New("netlink string contains an embedded NUL")
	}
	return string(value), nil
}

func marshalIfInfoMessage(index int, flags, change uint32) ([]byte, error) {
	if index < 0 || uint64(index) > math.MaxInt32 {
		return nil, errors.New("ifindex is outside the rtnetlink range")
	}
	data := make([]byte, unix.SizeofIfInfomsg)
	data[0] = unix.AF_UNSPEC
	binary.NativeEndian.PutUint32(data[4:8], uint32(index))
	binary.NativeEndian.PutUint32(data[8:12], flags)
	binary.NativeEndian.PutUint32(data[12:16], change)
	return data, nil
}

func appendRouteAttributes(data []byte, encode func(*netlink.AttributeEncoder) error) ([]byte, error) {
	encoder := netlink.NewAttributeEncoder()
	if err := encode(encoder); err != nil {
		return nil, err
	}
	attributes, err := encoder.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode route-link attributes: %w", err)
	}
	return append(data, attributes...), nil
}
