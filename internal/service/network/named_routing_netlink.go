package network

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

type observedNamedRouteRule struct {
	rule     namedRouteRule
	extra    bool
	inverted bool
}

func (observed observedNamedRouteRule) builtinLocal() bool {
	rule := observed.rule
	return !observed.extra && rule.priority == 0 && rule.table == unix.RT_TABLE_LOCAL &&
		rule.action == unix.FR_ACT_TO_TBL && !rule.source.IsValid() && rule.ingress == "" &&
		!rule.suppressDefault && (rule.protocol == unix.RTPROT_KERNEL || rule.protocol == unix.RTPROT_UNSPEC)
}

func (observed observedNamedRouteRule) couldMatch(want namedRouteRule) bool {
	if observed.inverted {
		return true
	}
	rule := observed.rule
	return (rule.ingress == "" || rule.ingress == want.ingress) &&
		(!rule.source.IsValid() || rule.source.Overlaps(want.source))
}

func decodeNamedRouteRule(message netlink.Message) (observedNamedRouteRule, error) {
	var observed observedNamedRouteRule
	data := message.Data
	if message.Header.Type != unix.RTM_NEWRULE || len(data) < ipv4RuleHeaderSize ||
		len(data) > routeL3MessageMax || data[0] != unix.AF_INET || data[2] > 32 {
		return observed, errors.New("invalid IPv4 policy rule dump message")
	}
	flags := binary.NativeEndian.Uint32(data[8:12])
	observed.inverted = flags&unix.FIB_RULE_INVERT != 0
	observed.extra = data[1] != 0 || data[3] != 0 || data[5] != 0 || data[6] != 0 ||
		flags&^uint32(unix.FIB_RULE_IIF_DETACHED) != 0
	rule := &observed.rule
	rule.table, rule.action = uint32(data[4]), data[7]
	decoder, err := netlink.NewAttributeDecoder(data[ipv4RuleHeaderSize:])
	if err != nil {
		return observed, err
	}
	seen := make(map[uint16]bool)
	for decoder.Next() {
		attr := decoder.Type()
		if seen[attr] {
			return observed, fmt.Errorf("duplicate IPv4 policy rule attribute %d", attr)
		}
		seen[attr] = true
		switch attr {
		case unix.FRA_PRIORITY:
			rule.priority = decoder.Uint32()
		case unix.FRA_TABLE:
			rule.table = decoder.Uint32()
		case unix.FRA_IIFNAME:
			name, err := decodeRouteCString(decoder.Bytes(), linuxInterfaceNameMax)
			if err != nil {
				return observed, err
			}
			rule.ingress = linkName(name)
		case unix.FRA_SRC:
			addr, err := decodeRouteIPv4(decoder.Bytes())
			if err != nil {
				return observed, err
			}
			rule.source = netip.PrefixFrom(addr, int(data[2]))
			if rule.source != rule.source.Masked() {
				return observed, errors.New("IPv4 policy rule source is not canonical")
			}
		case unix.FRA_PROTOCOL:
			rule.protocol = decoder.Uint8()
		case unix.FRA_SUPPRESS_PREFIXLEN:
			value := decoder.Uint32()
			rule.suppressDefault = value == 0
			observed.extra = observed.extra || (value != 0 && value != math.MaxUint32)
		case unix.FRA_SUPPRESS_IFGROUP:
			if decoder.Uint32() != math.MaxUint32 {
				observed.extra = true
			}
		default:
			observed.extra = true
		}
	}
	if err := decoder.Err(); err != nil {
		return observed, err
	}
	if data[2] != 0 && !rule.source.IsValid() {
		return observed, errors.New("IPv4 policy rule source address is missing")
	}
	return observed, nil
}

func (adapter *directRouteNetlink) changeNamedRule(
	ctx context.Context, fd int, rule namedRouteRule, create bool,
) error {
	data := make([]byte, ipv4RuleHeaderSize)
	data[0], data[2], data[7] = unix.AF_INET, uint8(rule.source.Bits()), rule.action
	if rule.table <= math.MaxUint8 {
		data[4] = uint8(rule.table)
	}
	data, err := appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.Uint32(unix.FRA_PRIORITY, rule.priority)
		encoder.String(unix.FRA_IIFNAME, rule.ingress.String())
		encoder.Bytes(unix.FRA_SRC, rule.source.Addr().AsSlice())
		encoder.Uint8(unix.FRA_PROTOCOL, rule.protocol)
		if rule.table > math.MaxUint8 {
			encoder.Uint32(unix.FRA_TABLE, rule.table)
		}
		if rule.suppressDefault {
			encoder.Uint32(unix.FRA_SUPPRESS_PREFIXLEN, 0)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return adapter.changeNamedRouting(ctx, fd, data, create, unix.RTM_NEWRULE, unix.RTM_DELRULE)
}

func (adapter *directRouteNetlink) changeNamedDefaultRoute(
	ctx context.Context, fd int, route routeIPv4Observation, create bool,
) error {
	data, err := marshalRouteMessage(0, unix.RT_TABLE_UNSPEC, route.protocol, route.scope, route.kind)
	if err != nil {
		return err
	}
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.Uint32(unix.RTA_TABLE, route.table)
		encoder.Uint32(unix.RTA_OIF, uint32(route.ifIndex))
		if route.hasGateway {
			encoder.Bytes(unix.RTA_GATEWAY, route.gateway.AsSlice())
		}
		return nil
	})
	if err != nil {
		return err
	}
	return adapter.changeNamedRouting(ctx, fd, data, create, unix.RTM_NEWROUTE, unix.RTM_DELROUTE)
}

func (adapter *directRouteNetlink) changeNamedRouting(
	ctx context.Context, fd int, data []byte, create bool, addType, deleteType netlink.HeaderType,
) error {
	command := deleteType
	flags := netlink.Request | netlink.Acknowledge
	if create {
		command = addType
		flags |= netlink.Create | netlink.Excl
	}
	_, err := adapter.execute(ctx, fd, netlink.Message{
		Header: netlink.Header{Type: command, Flags: flags}, Data: data,
	})
	return err
}
