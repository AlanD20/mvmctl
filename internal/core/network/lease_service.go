package network

import (
	"context"
	"fmt"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// LeaseService manages IP leases for a specific network.
// Matches src/mvmctl/core/network/_lease_service.py: LeaseService
type LeaseService struct {
	leaseRepo LeaseRepository
	net       *model.Network
}

// NewLeaseService creates a LeaseService from a *model.Network or string identifier.
// Matches Python: LeaseService(entity: str | NetworkItem, repo)
// Python's __init__ accepts both str and NetworkItem — if str, it resolves via
// NetworkResolver() which uses the default database. Go requires an explicit
// Repository for string resolution, and returns an error if networkRepo
// is nil when it's needed.
func NewLeaseService(ctx context.Context, entity any, leaseRepo LeaseRepository, networkRepo Repository) (*LeaseService, error) {
	switch e := entity.(type) {
	case *model.Network:
		return &LeaseService{
			leaseRepo: leaseRepo,
			net:       e,
		}, nil
	case string:
		if networkRepo == nil {
			return nil, errs.Wrap(errs.CodeNetworkNotFound,
				fmt.Errorf("cannot resolve network entity %q: no network repository provided", e))
		}
		resolver := NewResolver(networkRepo, nil)
		net, err := resolver.Resolve(ctx, e)
		if err != nil {
			return nil, err
		}
		return &LeaseService{
			leaseRepo: leaseRepo,
			net:       net,
		}, nil
	default:
		return nil, fmt.Errorf("expected *model.Network or string, got %T", entity)
	}
}

func (s *LeaseService) NetworkID() string {
	return s.net.ID
}

func (s *LeaseService) NetworkName() string {
	return s.net.Name
}

// GetLeases returns all IP leases for this network.
// Matches Python: returns new model.NetworkLeaseItem copies from db leases.
func (s *LeaseService) GetLeases(ctx context.Context) ([]*model.NetworkLeaseItem, error) {
	dbLeases, err := s.leaseRepo.ListAll(ctx, s.net.ID)
	if err != nil {
		return nil, err
	}
	result := make([]*model.NetworkLeaseItem, len(dbLeases))
	for i, l := range dbLeases {
		result[i] = &model.NetworkLeaseItem{
			NetworkID: l.NetworkID,
			IPv4:      l.IPv4,
			VMID:      l.VMID,
			ID:        l.ID,
			LeasedAt:  l.LeasedAt,
			ExpiresAt: l.ExpiresAt,
		}
	}
	return result, nil
}

// Get returns lease for a specific IP address.
// Matches Python: returns model.NetworkLeaseItem copy or nil.
func (s *LeaseService) Get(ctx context.Context, ip string) (*model.NetworkLeaseItem, error) {
	lease, err := s.leaseRepo.Get(ctx, s.net.ID, ip)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, nil
	}
	return &model.NetworkLeaseItem{
		NetworkID: lease.NetworkID,
		IPv4:      lease.IPv4,
		VMID:      lease.VMID,
		ID:        lease.ID,
		LeasedAt:  lease.LeasedAt,
		ExpiresAt: lease.ExpiresAt,
	}, nil
}

// GetByVMID returns all leases for a specific VM on this network.
// Matches Python's get_by_vm_id.
func (s *LeaseService) GetByVMID(ctx context.Context, vmID string) ([]*model.NetworkLeaseItem, error) {
	dbLeases, err := s.leaseRepo.ListByVM(ctx, s.net.ID, vmID)
	if err != nil {
		return nil, err
	}
	result := make([]*model.NetworkLeaseItem, len(dbLeases))
	for i, l := range dbLeases {
		result[i] = &model.NetworkLeaseItem{
			NetworkID: l.NetworkID,
			IPv4:      l.IPv4,
			VMID:      l.VMID,
			ID:        l.ID,
			LeasedAt:  l.LeasedAt,
			ExpiresAt: l.ExpiresAt,
		}
	}
	return result, nil
}

// IsAvailable checks if an IP address is available (not leased).
// Matches Python's is_available.
func (s *LeaseService) IsAvailable(ctx context.Context, ip string) (bool, error) {
	lease, err := s.leaseRepo.Get(ctx, s.net.ID, ip)
	if err != nil {
		return false, err
	}
	return lease == nil, nil
}

// Lease allocates the next available IP from this network's subnet.
// Matches Python's lease() with max_retries=10 and IntegrityError handling.
func (s *LeaseService) Lease(ctx context.Context, vmID string) (string, error) {
	maxRetries := 10
	var lastError error

	for attempt := 0; attempt < maxRetries; attempt++ {
		leases, err := s.GetLeases(ctx)
		if err != nil {
			return "", err
		}
		usedIPs := make([]string, len(leases))
		for i, l := range leases {
			usedIPs[i] = l.IPv4
		}

		allocatedIP, err := AllocateNextIP(usedIPs, s.net.Subnet, s.net.IPv4Gateway)
		if err != nil {
			return "", errs.Wrap(errs.CodeNetworkLeaseExhausted, err)
		}

		vmIDCopy := vmID
		lease, err := s.leaseRepo.Acquire(ctx, s.net.ID, allocatedIP, &vmIDCopy)
		if err != nil {
			return "", err
		}
		if lease != nil {
			return allocatedIP, nil
		}

		// IP was already taken (INSERT OR IGNORE returned 0 rows affected) — retry next candidate.
		lastError = fmt.Errorf("IP %s already allocated", allocatedIP)
		continue
	}

	if lastError != nil {
		return "", errs.NetworkError(
			fmt.Sprintf("Failed to allocate IP after %d attempts", maxRetries))
	}
	return "", errs.NetworkError(
		fmt.Sprintf("Failed to allocate IP after %d attempts", maxRetries))
}

// LeaseSpecific allocates a specific IP address from this network's subnet.
// Matches Python's lease_specific.
func (s *LeaseService) LeaseSpecific(ctx context.Context, ip, vmID string) (string, error) {
	available, err := s.IsAvailable(ctx, ip)
	if err != nil {
		return "", err
	}
	if !available {
		return "", errs.NetworkError(fmt.Sprintf("IP %s is already leased", ip))
	}

	vmIDCopy := vmID
	_, err = s.leaseRepo.Acquire(ctx, s.net.ID, ip, &vmIDCopy)
	if err != nil {
		return "", err
	}
	return ip, nil
}

// Release releases all leases for a VM from this network.
// Matches Python's release.
func (s *LeaseService) Release(ctx context.Context, vmID string) error {
	return s.leaseRepo.ReleaseByVM(ctx, vmID)
}
