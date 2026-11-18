package firewall

import (
	"net/netip"
	"time"
)

// FloatingIPBinding is a complete root-resolved resource projection. It is not a caller's association request.
// LeaseDuration is policy, not remaining time; neither loading nor compiling this binding installs a live element.
type FloatingIPBinding struct {
	OwnerUID              uint32        `json:"owner_uid"`
	ID                    string        `json:"id"`
	NetworkID             string        `json:"network_id"`
	IPv4                  netip.Addr    `json:"ipv4"`
	Generation            uint64        `json:"generation"`
	OwnerVMID             string        `json:"owner_vm_id"`
	OwnerLaunchGeneration uint64        `json:"owner_launch_generation"`
	LeaseDuration         time.Duration `json:"lease_duration_ns"`
}

func validateFloatingIPBindings(
	resources []FloatingIPBinding,
	networks map[string]NetworkIntent,
	reservations map[string]EndpointReservation,
) error {
	ids := make(map[string]struct{}, len(resources))
	addresses := make(map[string]struct{}, len(resources)+len(reservations))
	for _, reservation := range reservations {
		addresses[reservation.NetworkID+"/"+reservation.IPv4.String()] = struct{}{}
	}
	for _, resource := range resources {
		network, found := networks[resource.NetworkID]
		if !found || resource.OwnerUID != network.OwnerUID || !validResourceID(resource.ID) ||
			resource.Generation == 0 ||
			!usableIPv4Address(network.Subnet, resource.IPv4) ||
			resource.IPv4 == network.HostIPv4 {
			return invalidGeneration("durable floating-IP logical authority is invalid")
		}
		if _, found := ids[resource.ID]; found {
			return invalidGeneration("duplicate durable floating-IP identity")
		}
		key := resource.NetworkID + "/" + resource.IPv4.String()
		if _, found := addresses[key]; found {
			return invalidGeneration("durable floating-IP address conflicts with a reservation")
		}
		if resource.OwnerVMID == "" {
			if resource.OwnerLaunchGeneration != 0 || resource.LeaseDuration != 0 {
				return invalidGeneration("unassociated durable floating IP carries ownership authority")
			}
		} else {
			owner, found := reservations[resource.OwnerVMID]
			if !found || owner.OwnerUID != resource.OwnerUID || owner.NetworkID != resource.NetworkID ||
				resource.OwnerLaunchGeneration == 0 || resource.LeaseDuration <= 0 || resource.LeaseDuration%time.Second != 0 {
				return invalidGeneration("durable floating-IP owner or lease policy is invalid")
			}
		}
		ids[resource.ID] = struct{}{}
		addresses[key] = struct{}{}
	}
	return nil
}
