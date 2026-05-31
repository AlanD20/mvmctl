package testutil

import (
	"context"
	"sync"
	"time"

	"mvmctl/internal/core/network"
	"mvmctl/internal/infra/model"
)

// LeaseRepo is an in-memory lease repository for testing.
// Matches Python's mvmctl.core.network._repository.LeaseRepository exactly,
// including the count_available formula: total_hosts - gateway_count - lease_count.
//
// To use CountAvailable correctly, call SetNetwork(subnetTotalHosts, hasGateway)
// to match the subnet's usable host count. Defaults: subnetTotalHosts=254, hasGateway=true.
type LeaseRepo struct {
	mu               sync.RWMutex
	leases           []*model.NetworkLeaseItem
	nextID           int64
	subnetTotalHosts int  // total usable hosts in the subnet (from ipaddress.IPv4Network(network.subnet).hosts())
	hasGateway       bool // whether the gateway is set
}

func NewLeaseRepo() *LeaseRepo {
	return &LeaseRepo{
		leases:           make([]*model.NetworkLeaseItem, 0),
		nextID:           1,
		subnetTotalHosts: 254, // default /24
		hasGateway:       true,
	}
}

// SetNetwork configures the subnet parameters for CountAvailable calculations.
// totalHosts should match len(list(ipaddress.IPv4Network(subnet, strict=False).hosts())).
// hasGateway should be true if ipv4_gateway is set.
func (r *LeaseRepo) SetNetwork(totalHosts int, hasGateway bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subnetTotalHosts = totalHosts
	r.hasGateway = hasGateway
}

func (r *LeaseRepo) Get(_ context.Context, networkID string, ipv4 string) (*model.NetworkLeaseItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.leases {
		if l.NetworkID == networkID && l.IPv4 == ipv4 {
			return l, nil
		}
	}
	return nil, nil
}

func (r *LeaseRepo) ListAll(_ context.Context, networkID string) ([]*model.NetworkLeaseItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.NetworkLeaseItem
	for _, l := range r.leases {
		if l.NetworkID == networkID {
			result = append(result, l)
		}
	}
	return result, nil
}

func (r *LeaseRepo) ListByVM(_ context.Context, networkID string, vmID string) ([]*model.NetworkLeaseItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*model.NetworkLeaseItem
	for _, l := range r.leases {
		if l.NetworkID == networkID {
			if l.VMID != nil && *l.VMID == vmID {
				result = append(result, l)
			}
		}
	}
	return result, nil
}

func (r *LeaseRepo) ListAllBatch(_ context.Context, networkIDs []string) ([]*model.NetworkLeaseItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]bool)
	for _, id := range networkIDs {
		set[id] = true
	}
	var result []*model.NetworkLeaseItem
	for _, l := range r.leases {
		if set[l.NetworkID] {
			result = append(result, l)
		}
	}
	return result, nil
}

func (r *LeaseRepo) Acquire(
	_ context.Context,
	networkID string,
	ipv4 string,
	vmID *string,
) (*model.NetworkLeaseItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if already acquired
	for _, l := range r.leases {
		if l.NetworkID == networkID && l.IPv4 == ipv4 {
			return l, nil
		}
	}

	id := r.nextID
	r.nextID++
	now := time.Now().UTC().Format(time.RFC3339)
	lease := &model.NetworkLeaseItem{
		ID:        &id,
		NetworkID: networkID,
		IPv4:      ipv4,
		VMID:      vmID,
		LeasedAt:  now,
	}
	r.leases = append(r.leases, lease)
	return lease, nil
}

func (r *LeaseRepo) Release(_ context.Context, networkID string, ipv4 string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, l := range r.leases {
		if l.NetworkID == networkID && l.IPv4 == ipv4 {
			r.leases = append(r.leases[:i], r.leases[i+1:]...)
			break
		}
	}
	return nil
}

func (r *LeaseRepo) ReleaseByVM(_ context.Context, vmID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var kept []*model.NetworkLeaseItem
	for _, l := range r.leases {
		if l.VMID == nil || *l.VMID != vmID {
			kept = append(kept, l)
		}
	}
	r.leases = kept
	return nil
}

func (r *LeaseRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.leases), nil
}

// CountAvailable returns the number of available IP addresses in the network.
// Matches Python's exact formula: total_hosts - gateway_count - lease_count.
func (r *LeaseRepo) CountAvailable(_ context.Context, networkID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Count existing leases
	leaseCount := 0
	for _, l := range r.leases {
		if l.NetworkID == networkID {
			leaseCount++
		}
	}
	// Python formula from count_available:
	//   total_hosts = len(list(ipaddress.IPv4Network(subnet, strict=False).hosts()))
	//   gateway_count = 1 if gateway else 0
	//   available = total_hosts - gateway_count - lease_count
	gatewayCount := 0
	if r.hasGateway {
		gatewayCount = 1
	}
	available := r.subnetTotalHosts - gatewayCount - leaseCount
	if available < 0 {
		available = 0
	}
	return available, nil
}

// Ensure LeaseRepo implements network.LeaseRepository.
var _ network.LeaseRepository = (*LeaseRepo)(nil)
