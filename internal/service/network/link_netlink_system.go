package network

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

type routeNetlink interface {
	inspectLink(context.Context, int, linkName) (*routeLinkObservation, error)
	listLinks(context.Context, int) ([]routeLinkObservation, error)
	listIPv4Addresses(context.Context, int, int) ([]routeAddressObservation, error)
	listIPv4Routes(context.Context, int, int) ([]routeIPv4Observation, error)
	invalidateIPv4Neighbor(context.Context, int, int, netip.Addr) error
	createBridgeDown(context.Context, int, linkName, string) error
	createVethPairDown(context.Context, int, int, linkName, string, linkName, string) error
	addIPv4Address(context.Context, int, int, addressExpectation) error
	setLinkAlias(context.Context, int, int, string) error
	setLinkMaster(context.Context, int, int, int) error
	setLinkUp(context.Context, int, int, bool) error
	deleteLink(context.Context, int, int) error
}

type routeNetlinkConnection interface {
	Execute(netlink.Message) ([]netlink.Message, error)
	SetDeadline(time.Time) error
	Close() error
}

type directRouteNetlink struct {
	dial func(int, *netlink.Config) (routeNetlinkConnection, error)
}

func newDirectRouteNetlink() *directRouteNetlink {
	return &directRouteNetlink{dial: func(family int, config *netlink.Config) (routeNetlinkConnection, error) {
		return netlink.Dial(family, config)
	}}
}

func (adapter *directRouteNetlink) inspectLink(
	ctx context.Context,
	namespaceFD int,
	name linkName,
) (*routeLinkObservation, error) {
	data, err := marshalIfInfoMessage(0, 0, 0)
	if err != nil {
		return nil, err
	}
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.String(unix.IFLA_IFNAME, name.String())
		return nil
	})
	if err != nil {
		return nil, err
	}
	replies, err := adapter.execute(ctx, namespaceFD, netlink.Message{
		Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_GETLINK), Flags: netlink.Request},
		Data:   data,
	})
	if err != nil {
		if errors.Is(err, unix.ENODEV) {
			return nil, nil
		}
		return nil, err
	}
	if len(replies) != 1 {
		return nil, errors.New("route-link inspection returned an unexpected reply count")
	}
	observation, err := decodeRouteLinkMessage(replies[0])
	if err != nil {
		return nil, err
	}
	return &observation, nil
}

func (adapter *directRouteNetlink) listLinks(
	ctx context.Context,
	namespaceFD int,
) ([]routeLinkObservation, error) {
	data, err := marshalIfInfoMessage(0, 0, 0)
	if err != nil {
		return nil, err
	}
	replies, err := adapter.execute(ctx, namespaceFD, netlink.Message{
		Header: netlink.Header{
			Type: netlink.HeaderType(unix.RTM_GETLINK), Flags: netlink.Request | netlink.Dump,
		},
		Data: data,
	})
	if err != nil {
		return nil, err
	}
	observations := make([]routeLinkObservation, 0, len(replies))
	for index, reply := range replies {
		observation, decodeErr := decodeRouteLinkMessage(reply)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode route-link dump reply %d: %w", index, decodeErr)
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func (adapter *directRouteNetlink) createBridgeDown(
	ctx context.Context,
	namespaceFD int,
	name linkName,
	alias string,
) error {
	data, err := marshalIfInfoMessage(0, 0, 0)
	if err != nil {
		return err
	}
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.String(unix.IFLA_IFNAME, name.String())
		encoder.String(unix.IFLA_IFALIAS, alias)
		encoder.Nested(unix.IFLA_LINKINFO, func(linkInfo *netlink.AttributeEncoder) error {
			linkInfo.String(unix.IFLA_INFO_KIND, linkKindBridge)
			return nil
		})
		return nil
	})
	if err != nil {
		return err
	}
	return adapter.mutate(ctx, namespaceFD, unix.RTM_NEWLINK, netlink.Create|netlink.Excl, data)
}

func (adapter *directRouteNetlink) createVethPairDown(
	ctx context.Context,
	hostNamespaceFD int,
	peerNamespaceFD int,
	hostName linkName,
	hostAlias string,
	peerName linkName,
	peerAlias string,
) error {
	if peerNamespaceFD <= 0 {
		return errors.New("veth peer namespace descriptor is not explicit")
	}
	peerData, err := marshalIfInfoMessage(0, 0, 0)
	if err != nil {
		return err
	}
	peerData, err = appendRouteAttributes(peerData, func(peer *netlink.AttributeEncoder) error {
		peer.String(unix.IFLA_IFNAME, peerName.String())
		peer.String(unix.IFLA_IFALIAS, peerAlias)
		peer.Uint32(unix.IFLA_NET_NS_FD, uint32(peerNamespaceFD))
		return nil
	})
	if err != nil {
		return err
	}
	data, err := marshalIfInfoMessage(0, 0, 0)
	if err != nil {
		return err
	}
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.String(unix.IFLA_IFNAME, hostName.String())
		encoder.String(unix.IFLA_IFALIAS, hostAlias)
		encoder.Nested(unix.IFLA_LINKINFO, func(linkInfo *netlink.AttributeEncoder) error {
			linkInfo.String(unix.IFLA_INFO_KIND, linkKindVeth)
			linkInfo.Nested(unix.IFLA_INFO_DATA, func(veth *netlink.AttributeEncoder) error {
				veth.Do(netlink.Nested|vethInfoPeer, func() ([]byte, error) {
					return peerData, nil
				})
				return nil
			})
			return nil
		})
		return nil
	})
	if err != nil {
		return err
	}
	return adapter.mutate(ctx, hostNamespaceFD, unix.RTM_NEWLINK, netlink.Create|netlink.Excl, data)
}

func (adapter *directRouteNetlink) setLinkMaster(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
	masterIfIndex int,
) error {
	if masterIfIndex <= 0 {
		return errors.New("link master ifindex must be positive")
	}
	data, err := marshalIfInfoMessage(ifIndex, 0, 0)
	if err != nil {
		return err
	}
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.Uint32(unix.IFLA_MASTER, uint32(masterIfIndex))
		return nil
	})
	if err != nil {
		return err
	}
	return adapter.mutate(ctx, namespaceFD, unix.RTM_NEWLINK, 0, data)
}

func (adapter *directRouteNetlink) setLinkAlias(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
	alias string,
) error {
	data, err := marshalIfInfoMessage(ifIndex, 0, 0)
	if err != nil {
		return err
	}
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.String(unix.IFLA_IFALIAS, alias)
		return nil
	})
	if err != nil {
		return err
	}
	return adapter.mutate(ctx, namespaceFD, unix.RTM_NEWLINK, 0, data)
}

func (adapter *directRouteNetlink) deleteLink(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
) error {
	data, err := marshalIfInfoMessage(ifIndex, 0, 0)
	if err != nil {
		return err
	}
	return adapter.mutate(ctx, namespaceFD, unix.RTM_DELLINK, 0, data)
}

func (adapter *directRouteNetlink) mutate(
	ctx context.Context,
	namespaceFD int,
	messageType uint16,
	flags netlink.HeaderFlags,
	data []byte,
) error {
	replies, err := adapter.execute(ctx, namespaceFD, netlink.Message{
		Header: netlink.Header{
			Type: netlink.HeaderType(messageType), Flags: netlink.Request | netlink.Acknowledge | flags,
		},
		Data: data,
	})
	if err != nil {
		return err
	}
	return validateRouteMutationACK(replies)
}

func (adapter *directRouteNetlink) execute(
	ctx context.Context,
	namespaceFD int,
	message netlink.Message,
) (_ []netlink.Message, returnErr error) {
	if ctx == nil {
		return nil, errors.New("route-netlink context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if adapter == nil || adapter.dial == nil {
		return nil, errors.New("route-netlink adapter is incomplete")
	}
	if namespaceFD <= 0 {
		return nil, errors.New("route-netlink namespace descriptor is not explicit")
	}
	connection, err := adapter.dial(unix.NETLINK_ROUTE, &netlink.Config{NetNS: namespaceFD, Strict: true})
	if err != nil {
		return nil, fmt.Errorf("open route-netlink connection: %w", err)
	}
	var closeOnce sync.Once
	var closeErr error
	closeConnection := func() {
		closeOnce.Do(func() { closeErr = connection.Close() })
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			closeConnection()
		case <-done:
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			closeConnection()
			return nil, errors.Join(fmt.Errorf("set route-netlink deadline: %w", err), closeErr)
		}
	}
	replies, executeErr := connection.Execute(message)
	closeConnection()
	if executeErr != nil {
		return replies, errors.Join(executeErr, ctx.Err(), closeErr)
	}
	if closeErr != nil {
		return replies, fmt.Errorf("close route-netlink connection: %w", closeErr)
	}
	return replies, nil
}

func validateRouteMutationACK(replies []netlink.Message) error {
	if len(replies) != 1 {
		return errors.New("route-link mutation returned an unexpected acknowledgement count")
	}
	reply := replies[0]
	if reply.Header.Type != netlink.Error || reply.Header.Flags&netlink.Multi != 0 || len(reply.Data) < 4 ||
		binary.NativeEndian.Uint32(reply.Data[:4]) != 0 {
		return errors.New("route-link mutation returned a malformed acknowledgement")
	}
	return nil
}
