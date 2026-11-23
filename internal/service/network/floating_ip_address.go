package network

import "net/netip"

// selectFloatingIPv4 admits an explicit address or chooses the first unreserved usable address.
// The caller must hold the network and firewall mutation authorities while gathering BOTH primary and
// floating reservations and publishing the selected reservation. This pure selection is not a reservation.
func selectFloatingIPv4(
	network managedNetworkL3,
	requested string,
	reserved []netip.Addr,
) (ipv4Address, error) {
	if err := validateManagedNetworkL3(network); err != nil {
		return ipv4Address{}, err
	}
	prefix := network.prefix.value
	used := make(map[netip.Addr]struct{}, len(reserved)+1)
	used[network.gateway.value] = struct{}{}
	for _, address := range reserved {
		if !usableIPv4Address(prefix, address) || address == network.gateway.value {
			return ipv4Address{}, networkAuthorityCorruptError(
				"floating-IP reservation contains an invalid guest address",
				nil,
			)
		}
		used[address] = struct{}{}
	}
	if requested != "" {
		address, err := netip.ParseAddr(requested)
		if err != nil || address.String() != requested || !usableIPv4Address(prefix, address) {
			return ipv4Address{}, networkAuthorityValidationError(
				"floating IP must be a canonical usable IPv4 address in its network",
			)
		}
		if _, exists := used[address]; exists {
			return ipv4Address{}, networkAuthorityValidationError(
				"floating IP is already reserved or is the network gateway",
			)
		}
		return ipv4Address{value: address}, nil
	}
	// Each skipped candidate is in used. Runtime is bounded by recorded reservations, not subnet capacity.
	for candidate := prefix.Addr().Next(); usableIPv4Address(prefix, candidate); candidate = candidate.Next() {
		if _, exists := used[candidate]; !exists {
			return ipv4Address{value: candidate}, nil
		}
	}
	return ipv4Address{}, networkAuthorityValidationError("managed network has no available floating-IP address")
}
