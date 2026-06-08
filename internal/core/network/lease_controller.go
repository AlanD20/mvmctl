package network

import (
	"context"
	"fmt"

	"mvmctl/internal/lib/model"
	libnet "mvmctl/internal/lib/network"
	"mvmctl/pkg/errs"
)

// LeaseController manages IP leases for a specific network.
// Construction pattern matches Controller convention (per-entity binding).
// Python equivalent: _lease_service.py LeaseService
type LeaseController struct {
	leaseRepo LeaseRepository
	net       *model.Network
}

// NewLeaseController creates a LeaseController from a *model.Network or string identifier.
// Python equivalent: _lease_service.py LeaseService(entity, repo)
// Python's __init__ accepts both str and NetworkItem — if str, it resolves via
// NetworkResolver() which uses the default database. Go requires an explicit
// Repository for string resolution, and returns an error if networkRepo
// is nil when it's needed.
func NewLeaseController(
	ctx context.Context,
	entity any,
	leaseRepo LeaseRepository,
	networkRepo Repository,
) (*LeaseController, error) {
	switch e := entity.(type) {
	case *model.Network:
		return &LeaseController{
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
		return &LeaseController{
			leaseRepo: leaseRepo,
			net:       net,
		}, nil
	default:
		return nil, fmt.Errorf("expected *model.Network or string, got %T", entity)
	}
}

func (s *LeaseController) NetworkID() string {
	return s.net.ID
}

func (s *LeaseController) NetworkName() string {
	return s.net.Name
}

// GetLeases returns all IP leases for this network.
func (s *LeaseController) GetLeases(ctx context.Context) ([]*model.NetworkLeaseItem, error) {
	return s.leaseRepo.ListAll(ctx, s.net.ID)
}

// Get returns lease for a specific IP address.
func (s *LeaseController) Get(ctx context.Context, ip string) (*model.NetworkLeaseItem, error) {
	return s.leaseRepo.Get(ctx, s.net.ID, ip)
}

// GetByVMID returns all leases for a specific VM on this network.
func (s *LeaseController) GetByVMID(ctx context.Context, vmID string) ([]*model.NetworkLeaseItem, error) {
	return s.leaseRepo.ListByVM(ctx, s.net.ID, vmID)
}

// IsAvailable checks if an IP address is available (not leased).
// Matches Python's is_available.
func (s *LeaseController) IsAvailable(ctx context.Context, ip string) (bool, error) {
	lease, err := s.leaseRepo.Get(ctx, s.net.ID, ip)
	if err != nil {
		return false, err
	}
	return lease == nil, nil
}

// Lease allocates the next available IP from this network's subnet.
// Matches Python's lease() with max_retries=10 and IntegrityError handling.
func (s *LeaseController) Lease(ctx context.Context, vmID string) (string, error) {
	maxRetries := 10
	var lastError error

	for range maxRetries {
		leases, err := s.GetLeases(ctx)
		if err != nil {
			return "", err
		}
		usedIPs := make([]string, len(leases))
		for i, l := range leases {
			usedIPs[i] = l.IPv4
		}

		allocatedIP, err := libnet.AllocateNextIP(usedIPs, s.net.Subnet, s.net.IPv4Gateway)
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
		return "", errs.New(errs.CodeNetworkError,
			fmt.Sprintf("Failed to allocate IP after %d attempts", maxRetries))
	}
	return "", errs.New(errs.CodeNetworkError,
		fmt.Sprintf("Failed to allocate IP after %d attempts", maxRetries))
}

// LeaseSpecific allocates a specific IP address from this network's subnet.
// Matches Python's lease_specific.
func (s *LeaseController) LeaseSpecific(ctx context.Context, ip, vmID string) (string, error) {
	available, err := s.IsAvailable(ctx, ip)
	if err != nil {
		return "", err
	}
	if !available {
		return "", errs.New(errs.CodeNetworkError, fmt.Sprintf("IP %s is already leased", ip))
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
func (s *LeaseController) Release(ctx context.Context, vmID string) error {
	return s.leaseRepo.ReleaseByVM(ctx, vmID)
}
