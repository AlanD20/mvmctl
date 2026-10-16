package network

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// selectedUplink is kernel evidence, never a caller-selectable interface or route.
type selectedUplink struct {
	link       linkIdentity
	kind       string
	deviceType uint16
	alias      string
	master     int
	route      routeIPv4Observation
}

type uplinkLease struct {
	// mu prevents a retained namespace from closing during observation or use.
	mu       sync.Mutex
	host     *linkNamespaceLease
	route    *directRouteNetlink
	selected selectedUplink
	closed   bool
	closeErr error
}

func retainUplink(ctx context.Context, expected namespaceIdentity) (_ *uplinkLease, returnErr error) {
	namespace := newNamespaceEffect()
	host, err := retainHostLinkNamespace(ctx, namespace.deps, namespace.policy)
	if err != nil {
		return nil, networkAuthorityEffectError("retain NAT uplink host namespace", err)
	}
	defer func() {
		if returnErr != nil {
			returnErr = appendNetworkAuthorityError(returnErr, "release NAT uplink namespace", host.close(ctx))
		}
	}()
	fd, identity, err := host.descriptor()
	if err != nil || identity != expected {
		return nil, networkAuthorityCorruptError("NAT uplink host namespace does not match network authority", err)
	}
	route := newDirectRouteNetlink()
	selected, err := observeSelectedUplink(ctx, route, fd, identity)
	if err != nil {
		return nil, networkAuthorityEffectError("select NAT uplink", err)
	}
	lease := &uplinkLease{host: host, route: route, selected: selected}
	if err := lease.revalidate(ctx); err != nil {
		return nil, err
	}
	return lease, nil
}

func (lease *uplinkLease) revalidate(ctx context.Context) error {
	if lease == nil {
		return networkAuthorityCorruptError("NAT uplink authority is unavailable", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed || lease.host == nil {
		return networkAuthorityCorruptError("NAT uplink authority is released", nil)
	}
	fd, identity, err := lease.host.descriptor()
	if err != nil || identity != lease.selected.link.namespace {
		return networkAuthorityCorruptError("NAT uplink namespace identity changed", err)
	}
	observed, err := observeSelectedUplink(ctx, lease.route, fd, identity)
	if err != nil {
		return networkAuthorityEffectError("revalidate NAT uplink", err)
	}
	if observed != lease.selected {
		return networkAuthorityCorruptError("NAT uplink route or link identity changed", nil)
	}
	return nil
}

func (lease *uplinkLease) close(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.closed {
		lease.closeErr = lease.host.close(ctx)
		lease.host = nil
		lease.closed = true
	}
	return lease.closeErr
}

func observeSelectedUplink(
	ctx context.Context,
	adapter *directRouteNetlink,
	fd int,
	namespace namespaceIdentity,
) (selectedUplink, error) {
	routes, err := adapter.listDefaultIPv4Routes(ctx, fd)
	if err != nil {
		return selectedUplink{}, err
	}
	links, err := adapter.listLinks(ctx, fd)
	if err != nil {
		return selectedUplink{}, err
	}
	byIndex := make(map[int]routeLinkObservation, len(links))
	for _, link := range links {
		if _, exists := byIndex[link.ifIndex]; exists {
			return selectedUplink{}, errors.New("uplink link dump repeats an ifindex")
		}
		byIndex[link.ifIndex] = link
	}
	var selected selectedUplink
	count := 0
	for _, route := range routes {
		link, found := byIndex[route.ifIndex]
		if !found {
			return selectedUplink{}, errors.New("default route output link is missing")
		}
		if link.flags&unix.IFF_LOOPBACK != 0 || link.deviceType == unix.ARPHRD_LOOPBACK ||
			isManagedUplinkLink(link) {
			continue
		}
		count++
		selected = selectedUplink{
			link: linkIdentity{name: link.name, ifIndex: link.ifIndex, namespace: namespace},
			kind: link.kind, deviceType: link.deviceType, alias: link.alias, master: link.master, route: route,
		}
	}
	// CRITICAL: Metrics never resolve ambiguity; every eligible route counts, including equal-output routes.
	if count != 1 {
		return selectedUplink{}, fmt.Errorf("NAT requires exactly one eligible IPv4 default route, found %d", count)
	}
	return selected, nil
}

func isManagedUplinkLink(link routeLinkObservation) bool {
	if strings.HasPrefix(link.alias, "mvmctl:") {
		return true
	}
	for _, prefix := range []string{"mvb", "mvh", "mvn", "mvl", "mvt"} {
		if strings.HasPrefix(link.name.String(), prefix) {
			return true
		}
	}
	return false
}

func (adapter *directRouteNetlink) listDefaultIPv4Routes(
	ctx context.Context,
	fd int,
) ([]routeIPv4Observation, error) {
	data, err := marshalRouteMessage(
		0,
		unix.RT_TABLE_MAIN,
		unix.RTPROT_UNSPEC,
		unix.RT_SCOPE_UNIVERSE,
		unix.RTN_UNICAST,
	)
	if err != nil {
		return nil, err
	}
	replies, err := adapter.execute(ctx, fd, netlink.Message{
		Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_GETROUTE), Flags: netlink.Request | netlink.Dump},
		Data:   data,
	})
	if err != nil {
		return nil, err
	}
	var defaults []routeIPv4Observation
	for _, reply := range replies {
		if len(reply.Data) < unix.SizeofRtMsg {
			return nil, errors.New("default-route dump contains a truncated route")
		}
		if reply.Data[0] != unix.AF_INET || reply.Data[1] != 0 || reply.Data[7] != unix.RTN_UNICAST {
			continue
		}
		// Decode before filtering the table: RTA_TABLE can override the eight-bit header table.
		observation, err := decodeRouteIPv4Message(reply)
		if err != nil {
			return nil, err
		}
		if observation.table == unix.RT_TABLE_MAIN {
			defaults = append(defaults, observation)
		}
	}
	return defaults, nil
}
