package firewall

import (
	"context"
	"net/netip"
)

// ReservedNetworkIPv4 returns primary addresses retained by the lease's root-bound network.
// Stopped endpoints remain reserved even after their kernel topology has been cleaned.
func (lease *MutationLease) ReservedNetworkIPv4(ctx context.Context) ([]netip.Addr, error) {
	if lease == nil || lease.state == nil {
		return nil, generationAuthorityError("firewall reservation lease is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return nil, err
	}
	return reservedNetworkIPv4(state.intent, state.binding)
}

func reservedNetworkIPv4(intent Intent, binding EndpointBinding) ([]netip.Addr, error) {
	var addresses []netip.Addr
	for _, reservation := range intent.Reservations {
		if reservation.NetworkID == binding.NetworkID {
			if reservation.OwnerUID != binding.OwnerUID {
				return nil, generationAuthorityError(
					"firewall reservation owner differs from network authority",
					nil,
					nil,
				)
			}
			addresses = append(addresses, reservation.IPv4)
		}
	}
	return addresses, nil
}
