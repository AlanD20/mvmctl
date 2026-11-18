package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const guestControlAliasIOTimeout = 2 * time.Second

type guestControlAliasLink struct {
	Index int
	Name  string
	MAC   [6]byte
	Flags uint32
}
type guestControlAliasAddress struct {
	Index   int
	IP      netip.Addr
	Prefix  uint8
	Scope   uint8
	Flags   uint32
	Label   string
	Created uint32
}
type guestControlAliasRoute struct {
	Index  int
	Prefix netip.Prefix
	Source netip.Addr
}
type guestControlAliasSnapshot struct {
	Links     []guestControlAliasLink
	Addresses []guestControlAliasAddress
	Routes    []guestControlAliasRoute
}

// This closed effect seam represents only the observed alias operations; it carries no ready/authority hook.
type guestControlAliasKernel interface {
	snapshot(context.Context) (guestControlAliasSnapshot, error)
	add(context.Context, guestControlAliasAddress) error
	remove(context.Context, guestControlAliasAddress) error
}
type guestControlAliasNetlink struct{ namespace int }

func newGuestControlAliasNetlink() (*guestControlAliasNetlink, error) {
	fd, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	// Config.NetNS treats zero as a default/fallback selector, so preserve a strictly positive pinned descriptor.
	if fd == 0 {
		pinned, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = pinned
	}
	return &guestControlAliasNetlink{namespace: fd}, nil
}
func (kernel *guestControlAliasNetlink) close() error { return unix.Close(kernel.namespace) }
func (kernel *guestControlAliasNetlink) execute(
	ctx context.Context, message netlink.Message,
) ([]netlink.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, guestControlAliasIOTimeout)
	defer cancel()
	conn, err := netlink.Dial(unix.NETLINK_ROUTE, &netlink.Config{NetNS: kernel.namespace})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	replies, err := conn.Execute(message)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return replies, nil
}
func (kernel *guestControlAliasNetlink) snapshot(ctx context.Context) (guestControlAliasSnapshot, error) {
	result := guestControlAliasSnapshot{}
	links, err := kernel.execute(ctx, netlink.Message{
		Header: netlink.Header{Type: unix.RTM_GETLINK, Flags: netlink.Request | netlink.Dump},
		Data:   make([]byte, 16),
	})
	if err != nil {
		return result, err
	}
	for _, message := range links {
		if len(message.Data) < 16 {
			return result, errors.New("short alias link observation")
		}
		link := guestControlAliasLink{
			Index: int(binary.NativeEndian.Uint32(message.Data[4:8])),
			Flags: binary.NativeEndian.Uint32(message.Data[8:12]),
		}
		attrs, err := netlink.UnmarshalAttributes(message.Data[16:])
		if err != nil {
			return result, err
		}
		for _, attr := range attrs {
			switch attr.Type {
			case unix.IFLA_IFNAME:
				link.Name, err = guestControlAliasString(attr.Data)
			case unix.IFLA_ADDRESS:
				if len(attr.Data) == 6 {
					copy(link.MAC[:], attr.Data)
				}
			}
			if err != nil {
				return result, err
			}
		}
		result.Links = append(result.Links, link)
	}
	addresses, err := kernel.execute(ctx, netlink.Message{
		Header: netlink.Header{Type: unix.RTM_GETADDR, Flags: netlink.Request | netlink.Dump},
		Data:   []byte{unix.AF_INET, 0, 0, 0, 0, 0, 0, 0},
	})
	if err != nil {
		return result, err
	}
	for _, message := range addresses {
		address, err := decodeGuestControlAliasAddress(message.Data)
		if err != nil {
			return result, err
		}
		result.Addresses = append(result.Addresses, address)
	}
	routes, err := kernel.execute(ctx, netlink.Message{
		Header: netlink.Header{Type: unix.RTM_GETROUTE, Flags: netlink.Request | netlink.Dump},
		Data:   []byte{unix.AF_INET, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	})
	if err != nil {
		return result, err
	}
	for _, message := range routes {
		data := message.Data
		if len(data) < 12 || data[0] != unix.AF_INET {
			return result, errors.New("short alias route observation")
		}
		if data[4] != unix.RT_TABLE_MAIN || data[6] != unix.RT_SCOPE_LINK || data[7] != unix.RTN_UNICAST {
			continue
		}
		route := guestControlAliasRoute{}
		var destination netip.Addr
		gateway := false
		attrs, err := netlink.UnmarshalAttributes(data[12:])
		if err != nil {
			return result, err
		}
		for _, attr := range attrs {
			switch attr.Type {
			case unix.RTA_DST:
				destination, err = guestControlAliasIPv4(attr.Data)
			case unix.RTA_PREFSRC:
				route.Source, err = guestControlAliasIPv4(attr.Data)
			case unix.RTA_OIF:
				if len(attr.Data) != 4 {
					err = errors.New("invalid alias route index")
				} else {
					route.Index = int(binary.NativeEndian.Uint32(attr.Data))
				}
			case unix.RTA_GATEWAY:
				gateway = true
			}
			if err != nil {
				return result, err
			}
		}
		if !gateway && destination.Is4() && data[1] <= 32 && route.Index > 0 {
			route.Prefix = netip.PrefixFrom(destination, int(data[1])).Masked()
			result.Routes = append(result.Routes, route)
		}
	}
	return result, nil
}
func decodeGuestControlAliasAddress(data []byte) (guestControlAliasAddress, error) {
	if len(data) < 8 || data[0] != unix.AF_INET || data[1] > 32 {
		return guestControlAliasAddress{}, errors.New("invalid alias address header")
	}
	address := guestControlAliasAddress{
		Index: int(binary.NativeEndian.Uint32(data[4:8])), Prefix: data[1], Scope: data[3], Flags: uint32(data[2]),
	}
	attrs, err := netlink.UnmarshalAttributes(data[8:])
	if err != nil {
		return address, err
	}
	var peer netip.Addr
	for _, attr := range attrs {
		switch attr.Type {
		case unix.IFA_LOCAL:
			address.IP, err = guestControlAliasIPv4(attr.Data)
		case unix.IFA_ADDRESS:
			peer, err = guestControlAliasIPv4(attr.Data)
		case unix.IFA_LABEL:
			address.Label, err = guestControlAliasString(attr.Data)
		case unix.IFA_FLAGS:
			if len(attr.Data) != 4 {
				err = errors.New("invalid alias address flags")
			} else {
				address.Flags = binary.NativeEndian.Uint32(attr.Data)
			}
		case unix.IFA_CACHEINFO:
			if len(attr.Data) != 16 {
				err = errors.New("invalid alias cache information")
			} else {
				address.Created = binary.NativeEndian.Uint32(attr.Data[8:12])
			}
		}
		if err != nil {
			return address, err
		}
	}
	if !address.IP.IsValid() {
		address.IP = peer
	}
	if !address.IP.Is4() || address.Index <= 0 {
		return address, errors.New("alias address lacks exact IPv4 identity")
	}
	return address, nil
}
func guestControlAliasString(raw []byte) (string, error) {
	if len(raw) < 1 || raw[len(raw)-1] != 0 {
		return "", errors.New("unterminated alias label")
	}
	for _, value := range raw[:len(raw)-1] {
		if value == 0 {
			return "", errors.New("ambiguous alias label")
		}
	}
	return string(raw[:len(raw)-1]), nil
}
func guestControlAliasIPv4(raw []byte) (netip.Addr, error) {
	if len(raw) != 4 {
		return netip.Addr{}, errors.New("invalid alias IPv4 observation")
	}
	return netip.AddrFrom4([4]byte(raw)), nil
}
func marshalGuestControlAliasAddress(address guestControlAliasAddress) ([]byte, error) {
	if !address.IP.Is4() || address.Index <= 0 || address.Prefix > 32 || len(address.Label) > 15 {
		return nil, errors.New("invalid exact alias address")
	}
	data := make([]byte, 8)
	data[0] = unix.AF_INET
	data[1] = address.Prefix
	data[2] = byte(address.Flags)
	data[3] = address.Scope
	binary.NativeEndian.PutUint32(data[4:8], uint32(address.Index))
	ipv4 := address.IP.As4()
	encoder := netlink.NewAttributeEncoder()
	encoder.ByteOrder = binary.NativeEndian
	encoder.Bytes(unix.IFA_LOCAL, ipv4[:])
	encoder.Bytes(unix.IFA_ADDRESS, ipv4[:])
	encoder.String(unix.IFA_LABEL, address.Label)
	encoder.Uint32(unix.IFA_FLAGS, address.Flags)
	attrs, err := encoder.Encode()
	if err != nil {
		return nil, err
	}
	return append(data, attrs...), nil
}
func (kernel *guestControlAliasNetlink) add(ctx context.Context, address guestControlAliasAddress) error {
	data, err := marshalGuestControlAliasAddress(address)
	if err != nil {
		return err
	}
	_, err = kernel.execute(ctx, netlink.Message{
		Header: netlink.Header{
			Type:  unix.RTM_NEWADDR,
			Flags: netlink.Request | netlink.Acknowledge | netlink.Create | netlink.Excl,
		},
		Data: data,
	})
	return err
}
func (kernel *guestControlAliasNetlink) remove(ctx context.Context, address guestControlAliasAddress) error {
	data, err := marshalGuestControlAliasAddress(address)
	if err != nil {
		return err
	}
	_, err = kernel.execute(ctx, netlink.Message{
		Header: netlink.Header{Type: unix.RTM_DELADDR, Flags: netlink.Request | netlink.Acknowledge},
		Data:   data,
	})
	return err
}
