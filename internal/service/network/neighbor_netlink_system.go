package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

func (adapter *directRouteNetlink) inspectIPv4Neighbor(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
	address netip.Addr,
) (*routeNeighborObservation, error) {
	data, err := marshalIPv4NeighborMessage(ifIndex, address)
	if err != nil {
		return nil, err
	}
	replies, err := adapter.execute(ctx, namespaceFD, netlink.Message{
		Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_GETNEIGH), Flags: netlink.Request},
		Data:   data,
	})
	if onlyNeighborNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(replies) != 1 {
		return nil, errors.New("exact neighbor inspection returned an unexpected reply count")
	}
	observation, err := decodeIPv4NeighborMessage(replies[0])
	if err != nil {
		return nil, err
	}
	if observation.ifIndex != ifIndex || observation.address != address {
		return nil, errors.New("exact neighbor inspection returned a different bridge or address")
	}
	return &observation, nil
}

// invalidateIPv4Neighbor removes only one dynamic guest-address neighbor on one admitted bridge.
// Static, proxy, foreign, and malformed entries are conflicts, not cleanup targets.
func (adapter *directRouteNetlink) invalidateIPv4Neighbor(
	ctx context.Context,
	namespaceFD int,
	ifIndex int,
	address netip.Addr,
) error {
	observed, err := adapter.inspectIPv4Neighbor(ctx, namespaceFD, ifIndex, address)
	if err != nil || observed == nil {
		return err
	}
	if err := admitDynamicIPv4Neighbor(*observed, ifIndex, address); err != nil {
		return err
	}
	data, err := marshalIPv4NeighborMessage(ifIndex, address)
	if err != nil {
		return err
	}
	deleteErr := adapter.mutate(ctx, namespaceFD, unix.RTM_DELNEIGH, 0, data)
	inspectionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	remaining, inspectErr := adapter.inspectIPv4Neighbor(inspectionCtx, namespaceFD, ifIndex, address)
	if remaining == nil && inspectErr == nil {
		if onlyNeighborNotFound(deleteErr) {
			return nil
		}
		return deleteErr
	}
	return errors.Join(deleteErr, inspectErr, fmt.Errorf("exact IPv4 neighbor remains after invalidation"))
}

// onlyNeighborNotFound refuses joined cancellation or connection failures even when one cause is ENOENT.
func onlyNeighborNotFound(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyNeighborNotFound(cause) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return onlyNeighborNotFound(wrapped)
	}
	return err == unix.ENOENT
}
