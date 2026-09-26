package network

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	networklessIFAProto            = 11 // IFA_PROTO from linux/if_addr.h; not exported by x/sys/unix.
	networklessKernelLoopbackProto = 1  // IFAPROT_KERNEL_LO from linux/if_addr.h.
)

// inspectNetworklessNamespace observes complete link and IP authority through a socket bound to the retained
// namespace. A filtered per-interface dump would miss a foreign route or address attached to another link.
func inspectNetworklessNamespace(
	ctx context.Context, effect *namespaceEffect, pin NamespacePin, up bool,
) (index int, returnErr error) {
	retained, err := effect.retainLinkNamespace(ctx, pin)
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, retained.close(context.WithoutCancel(ctx))) }()
	fd, _, err := retained.descriptor()
	if err != nil {
		return 0, err
	}
	route := newDirectRouteNetlink()
	links, err := route.listLinks(ctx, fd)
	if err != nil {
		return 0, err
	}
	const loopbackFlags = unix.IFF_LOOPBACK | unix.IFF_UP | unix.IFF_RUNNING | unix.IFF_LOWER_UP
	if len(links) != 1 || validateExactLoopback(links[0]) != nil ||
		links[0].flags&^loopbackFlags != 0 || (links[0].flags&unix.IFF_UP != 0) != up {
		return 0, errors.New("networkless namespace is not exactly one loopback link in the recorded state")
	}
	lo := links[0]
	link := newLinkEffect(effect)
	if err := link.verifyCanonicalLoopbackAuthority(ctx, fd, lo, up); err != nil {
		return 0, err
	}
	if err := inspectNetworklessIPv6(ctx, route, fd, lo.ifIndex, up); err != nil {
		return 0, err
	}
	return lo.ifIndex, nil
}

func inspectNetworklessIPv6(ctx context.Context, route *directRouteNetlink, fd, ifIndex int, up bool) error {
	addressRequest := make([]byte, unix.SizeofIfAddrmsg)
	addressRequest[0] = unix.AF_UNSPEC
	addresses, err := route.execute(ctx, fd, netlink.Message{
		Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_GETADDR), Flags: netlink.Request | netlink.Dump},
		Data:   addressRequest,
	})
	if err != nil {
		return err
	}
	var ipv4Count, ipv6Count int
	for _, message := range addresses {
		if message.Header.Type != netlink.HeaderType(unix.RTM_NEWADDR) ||
			len(message.Data) < unix.SizeofIfAddrmsg || len(message.Data) > routeL3MessageMax ||
			int(binary.NativeEndian.Uint32(message.Data[4:8])) != ifIndex {
			return errors.New("networkless namespace has foreign or malformed address authority")
		}
		switch message.Data[0] {
		case unix.AF_INET:
			observation, decodeErr := decodeRouteAddressMessage(message)
			if decodeErr != nil || observation.prefix != canonicalLoopbackL3.address.prefix ||
				observation.scope != unix.RT_SCOPE_HOST {
				return errors.New("networkless IPv4 address differs from canonical loopback")
			}
			ipv4Count++
		case unix.AF_INET6:
			if err := inspectNetworklessIPv6Address(message); err != nil {
				return err
			}
			ipv6Count++
		default:
			return errors.New("networkless namespace has non-IP address authority")
		}
	}
	if ipv4Count != 1 || ipv6Count != 1 {
		return errors.New("networkless namespace loopback address set is incomplete or duplicated")
	}
	routeRequest := make([]byte, unix.SizeofRtMsg)
	routeRequest[0] = unix.AF_UNSPEC
	routes, err := route.execute(ctx, fd, netlink.Message{
		Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_GETROUTE), Flags: netlink.Request | netlink.Dump},
		Data:   routeRequest,
	})
	if err != nil {
		return err
	}
	var ipv4Routes []routeIPv4Observation
	var ipv6Routes int
	for _, message := range routes {
		if message.Header.Type != netlink.HeaderType(unix.RTM_NEWROUTE) ||
			len(message.Data) < unix.SizeofRtMsg || len(message.Data) > routeL3MessageMax {
			return errors.New("networkless namespace has malformed route authority")
		}
		switch message.Data[0] {
		case unix.AF_INET:
			observation, decodeErr := decodeRouteIPv4Message(message)
			if decodeErr != nil || observation.ifIndex != ifIndex {
				return errors.New("networkless namespace has foreign IPv4 route")
			}
			ipv4Routes = append(ipv4Routes, observation)
		case unix.AF_INET6:
			if err := inspectNetworklessIPv6Route(message, ifIndex); err != nil {
				return err
			}
			ipv6Routes++
		default:
			return errors.New("networkless namespace has non-IP route authority")
		}
	}
	if (up && ipv6Routes != 1) || (!up && ipv6Routes > 1) {
		return errors.New("networkless IPv6 loopback route set is incomplete or duplicated")
	}
	wantRoutes := []routeExpectation{canonicalLoopbackL3.localRoute}
	if up {
		wantRoutes = append(wantRoutes, canonicalLoopbackL3.connectedRoute,
			canonicalLoopbackL3.subnetBroadcastRoute)
	}
	return validateExactIPv4Authority(
		[]routeAddressObservation{{ifIndex: ifIndex, prefix: canonicalLoopbackL3.address.prefix,
			scope: unix.RT_SCOPE_HOST}}, ipv4Routes, canonicalLoopbackL3.address, wantRoutes, ifIndex,
	)
}

func inspectNetworklessIPv6Address(message netlink.Message) error {
	if message.Data[1] != 128 || message.Data[3] != unix.RT_SCOPE_HOST {
		return errors.New("networkless IPv6 address differs from ::1/128")
	}
	decoder, err := netlink.NewAttributeDecoder(message.Data[unix.SizeofIfAddrmsg:])
	if err != nil {
		return err
	}
	var local, peer netip.Addr
	for decoder.Next() {
		switch decoder.Type() {
		case unix.IFA_LOCAL:
			value, ok := netip.AddrFromSlice(decoder.Bytes())
			if !ok || !value.Is6() {
				return errors.New("invalid IPv6 local address")
			}
			local = value
		case unix.IFA_ADDRESS:
			value, ok := netip.AddrFromSlice(decoder.Bytes())
			if !ok || !value.Is6() {
				return errors.New("invalid IPv6 peer address")
			}
			peer = value
		case unix.IFA_FLAGS, unix.IFA_CACHEINFO:
		case networklessIFAProto:
			protocol := decoder.Bytes()
			if len(protocol) != 1 || protocol[0] != networklessKernelLoopbackProto {
				return errors.New("networkless IPv6 address protocol is not kernel loopback")
			}
		default:
			return errors.New("networkless IPv6 address has foreign attributes")
		}
	}
	if decoder.Err() != nil || peer != netip.IPv6Loopback() || (local.IsValid() && local != peer) {
		return errors.New("networkless IPv6 address is not exactly ::1")
	}
	return nil
}

func inspectNetworklessIPv6Route(message netlink.Message, ifIndex int) error {
	data := message.Data
	if data[1] != 128 || data[2] != 0 || data[3] != 0 || data[4] != unix.RT_TABLE_LOCAL ||
		data[5] != unix.RTPROT_KERNEL || data[6] != unix.RT_SCOPE_HOST || data[7] != unix.RTN_LOCAL ||
		binary.NativeEndian.Uint32(data[8:12]) != 0 {
		return errors.New("networkless IPv6 route is not canonical local loopback")
	}
	decoder, err := netlink.NewAttributeDecoder(data[unix.SizeofRtMsg:])
	if err != nil {
		return err
	}
	var destination netip.Addr
	var output int
	seen := make(map[uint16]bool, 4)
	for decoder.Next() {
		attribute := decoder.Type()
		if seen[attribute] && attribute != unix.RTA_CACHEINFO && attribute != unix.RTA_PAD {
			return errors.New("networkless IPv6 route repeats authority")
		}
		seen[attribute] = true
		switch attribute {
		case unix.RTA_DST:
			value, ok := netip.AddrFromSlice(decoder.Bytes())
			if !ok || !value.Is6() {
				return errors.New("invalid IPv6 route destination")
			}
			destination = value
		case unix.RTA_OIF:
			output = int(decoder.Uint32())
		case unix.RTA_TABLE:
			if decoder.Uint32() != unix.RT_TABLE_LOCAL {
				return errors.New("networkless IPv6 route table changed")
			}
		case unix.RTA_PREFSRC:
			value, ok := netip.AddrFromSlice(decoder.Bytes())
			if !ok || value != netip.IPv6Loopback() {
				return errors.New("networkless IPv6 route source changed")
			}
		case unix.RTA_PRIORITY:
			if decoder.Uint32() != 0 {
				return errors.New("networkless IPv6 route metric changed")
			}
		case unix.RTA_PREF:
			value := decoder.Bytes()
			if len(value) != 1 || value[0] != 0 {
				return errors.New("networkless IPv6 route preference changed")
			}
		case unix.RTA_CACHEINFO, unix.RTA_PAD:
		default:
			return errors.New("networkless IPv6 route has foreign authority")
		}
	}
	if decoder.Err() != nil || destination != netip.IPv6Loopback() || output != ifIndex {
		return errors.New("networkless IPv6 route differs from ::1/128 on loopback")
	}
	return nil
}
