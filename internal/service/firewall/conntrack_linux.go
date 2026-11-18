package firewall

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	conntrackNew           = netlink.HeaderType(1 << 8)
	conntrackGet           = conntrackNew | 1
	conntrackDelete        = conntrackNew | 2
	conntrackTupleOriginal = uint16(1)
	conntrackTupleReply    = uint16(2)
	conntrackID            = uint16(12)
	conntrackZone          = uint16(18)
)

type conntrackFlow struct {
	original  []byte
	zone      []byte
	id        []byte
	addresses [4]netip.Addr
}

// The tuple is preserved byte-for-byte, including protocol-specific keys and directional zones.
// Linux's delete path checks CTA_ID after tuple lookup, protecting a replacement using the same tuple.
// https://github.com/torvalds/linux/blob/v6.19/net/netfilter/nf_conntrack_netlink.c
func decodeConntrackFlow(data []byte) (conntrackFlow, error) {
	var flow conntrackFlow
	if len(data) < 4 || data[0] != unix.AF_INET || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return flow, invalidKernelObservation("conntrack IPv4 header is invalid")
	}
	attrs, err := conntrackAttributes(data[4:])
	if err != nil {
		return flow, err
	}
	for _, attr := range attrs {
		switch attr.Type & 0x3fff {
		case conntrackTupleOriginal, conntrackTupleReply:
			if attr.Type != (attr.Type&0x3fff)|netlink.Nested {
				return flow, invalidKernelObservation("conntrack tuple flags are invalid")
			}
			addresses, err := decodeConntrackTuple(attr.Data)
			if err != nil {
				return flow, err
			}
			if attr.Type&0x3fff == conntrackTupleOriginal {
				flow.original = bytes.Clone(attr.Data)
				copy(flow.addresses[:2], addresses[:])
			} else {
				copy(flow.addresses[2:], addresses[:])
			}
		case conntrackID:
			if attr.Type != conntrackID || len(attr.Data) != 4 {
				return flow, invalidKernelObservation("conntrack identity is invalid")
			}
			flow.id = bytes.Clone(attr.Data)
		case conntrackZone:
			if attr.Type != conntrackZone || len(attr.Data) != 2 {
				return flow, invalidKernelObservation("conntrack zone is invalid")
			}
			flow.zone = bytes.Clone(attr.Data)
		}
	}
	if len(flow.original) == 0 || len(flow.id) != 4 || !flow.addresses[2].Is4() {
		return flow, invalidKernelObservation("conntrack tuple or identity is missing")
	}
	if flow.zone == nil {
		flow.zone = []byte{0, 0}
	}
	return flow, nil
}

func decodeConntrackTuple(data []byte) ([2]netip.Addr, error) {
	var addresses [2]netip.Addr
	attrs, err := conntrackAttributes(data)
	if err != nil {
		return addresses, err
	}
	hasProtocol := false
	for _, attr := range attrs {
		switch attr.Type {
		case netlink.Nested | 1:
			ips, err := conntrackAttributes(attr.Data)
			if err != nil {
				return addresses, err
			}
			if len(ips) != 2 {
				return addresses, invalidKernelObservation("conntrack IPv4 tuple fields are invalid")
			}
			for _, ip := range ips {
				if ip.Type < 1 || ip.Type > 2 || len(ip.Data) != 4 {
					return addresses, invalidKernelObservation("conntrack IPv4 address is invalid")
				}
				addresses[ip.Type-1] = netip.AddrFrom4([4]byte(ip.Data))
			}
		case netlink.Nested | 2:
			proto, err := conntrackAttributes(attr.Data)
			if err != nil {
				return addresses, err
			}
			// All kernel IPv4 tuple forms use NUM, optional ports (including GRE keys), or ICMP keys.
			// Preserve these keys rather than narrowing the operation to TCP/UDP.
			number := -1
			fields := make(map[uint16]int, len(proto))
			for _, field := range proto {
				fields[field.Type] = len(field.Data)
				switch field.Type {
				case 1:
					if len(field.Data) != 1 {
						return addresses, invalidKernelObservation("conntrack protocol number is invalid")
					}
					number = int(field.Data[0])
				case 2, 3, 4:
					if len(field.Data) != 2 {
						return addresses, invalidKernelObservation("conntrack protocol key is invalid")
					}
				case 5, 6:
					if len(field.Data) != 1 {
						return addresses, invalidKernelObservation("conntrack ICMP key is invalid")
					}
				default:
					return addresses, invalidKernelObservation("conntrack IPv4 protocol field is unknown")
				}
			}
			switch number {
			case 1:
				hasProtocol = len(fields) == 4 && fields[4] == 2 && fields[5] == 1 && fields[6] == 1
			case 6, 17, 33, 47, 132, 136:
				hasProtocol = len(fields) == 3 && fields[2] == 2 && fields[3] == 2
			default:
				hasProtocol = number >= 0 && len(fields) == 1
			}
		case 3:
			if len(attr.Data) != 2 {
				return addresses, invalidKernelObservation("conntrack tuple zone is invalid")
			}
		default:
			return addresses, invalidKernelObservation("conntrack tuple field is unknown")
		}
	}
	if !addresses[0].Is4() || !addresses[1].Is4() || !hasProtocol {
		return addresses, invalidKernelObservation("conntrack tuple is incomplete")
	}
	return addresses, nil
}

func conntrackAttributes(data []byte) ([]netlink.Attribute, error) {
	var attrs []netlink.Attribute
	seen := make(map[uint16]bool)
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, invalidKernelObservation("conntrack attribute header is truncated")
		}
		n := int(binary.NativeEndian.Uint16(data[:2]))
		kind := binary.NativeEndian.Uint16(data[2:4])
		aligned := (n + 3) &^ 3
		if n < 4 ||
			aligned > len(data) ||
			kind&0x3fff == 0 ||
			seen[kind&0x3fff] ||
			len(attrs) >= maxKernelObservationAttributes ||
			!bytes.Equal(data[n:aligned], make([]byte, aligned-n)) {
			return nil, invalidKernelObservation("conntrack attribute framing is invalid")
		}
		seen[kind&0x3fff] = true
		attrs = append(attrs, netlink.Attribute{Type: kind, Data: bytes.Clone(data[4:n])})
		data = data[aligned:]
	}
	return attrs, nil
}

func (flow conntrackFlow) matches(address netip.Addr) bool {
	for _, candidate := range flow.addresses {
		if candidate == address {
			return true
		}
	}
	return false
}

func (flow conntrackFlow) deleteRequest() (netlink.Message, error) {
	// Revalidate the retained selector before creating the only mutation request shape.
	if _, err := decodeConntrackTuple(flow.original); err != nil {
		return netlink.Message{}, err
	}
	if len(flow.zone) != 2 || len(flow.id) != 4 {
		return netlink.Message{}, invalidKernelObservation("exact conntrack delete identity is incomplete")
	}
	attrs, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: netlink.Nested | conntrackTupleOriginal, Data: flow.original},
		{Type: conntrackZone, Data: flow.zone},
		{Type: conntrackID, Data: flow.id},
	})
	if err != nil {
		return netlink.Message{}, err
	}
	return netlink.Message{
		Header: netlink.Header{Type: conntrackDelete, Flags: netlink.Request | netlink.Acknowledge},
		Data:   append([]byte{unix.AF_INET, 0, 0, 0}, attrs...),
	}, nil
}

// newConntrackTransport uses only the already retained host namespace. Readiness and snapshot
// transports retain their closed read-only request grammar.
func (lease *kernelObservationNamespaceLease) newConntrackTransport(
	ctx context.Context,
) (*kernelObservationTransport, error) {
	if lease == nil || lease.state == nil {
		return nil, generationAuthorityError("conntrack namespace authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active || state.released {
		return nil, generationAuthorityError("conntrack namespace authority is released", nil, nil)
	}
	identity, err := inspectKernelObservationNamespace(ctx, state.deps, state.fd)
	if err != nil {
		return nil, err
	}
	if identity != state.identity {
		return nil, generationAuthorityError("conntrack retained namespace changed", nil, nil)
	}
	connection, err := state.deps.dial(ctx, unix.NETLINK_NETFILTER, &netlink.Config{NetNS: state.fd, Strict: true})
	if err != nil {
		if connection != nil {
			err = errors.Join(err, connection.Close())
		}
		return nil, err
	}
	if connection == nil {
		return nil, generationAuthorityError("conntrack socket is unavailable", nil, nil)
	}
	deadline, ok := connection.(interface{ SetDeadline(time.Time) error })
	if !ok {
		return nil, errors.Join(
			generationAuthorityError("conntrack socket deadline is unavailable", nil, nil),
			connection.Close(),
		)
	}
	if err := deadline.SetDeadline(kernelApplicationSocketDeadline(ctx, time.Now())); err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	return newKernelObservationTransport(connection, nil), nil
}
