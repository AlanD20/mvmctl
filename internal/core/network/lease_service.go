package network

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"

	"mvmctl/internal/infra/errs"
)

// LeaseService manages IP leases for a specific network.
// Matches src/mvmctl/core/network/_lease_service.py: LeaseService
type LeaseService struct {
	leaseRepo LeaseRepository
	net       *Network
}

// NewLeaseService creates a LeaseService from a *Network or string identifier.
// Matches Python: LeaseService(entity: str | NetworkItem, repo)
// Python's __init__ accepts both str and NetworkItem — if str, it resolves via
// NetworkResolver() which uses the default database. Go requires an explicit
// Repository for string resolution, and returns an error if networkRepo
// is nil when it's needed.
func NewLeaseService(entity interface{}, leaseRepo LeaseRepository, networkRepo Repository) (*LeaseService, error) {
	switch e := entity.(type) {
	case *Network:
		return &LeaseService{
			leaseRepo: leaseRepo,
			net:       e,
		}, nil
	case string:
		if networkRepo == nil {
			return nil, errs.Wrap(errs.CodeNetworkNotFound,
				fmt.Errorf("cannot resolve network entity %q: no network repository provided", e))
		}
		resolver := NewResolver(networkRepo)
		net, err := resolver.Resolve(context.Background(), e)
		if err != nil {
			return nil, err
		}
		return &LeaseService{
			leaseRepo: leaseRepo,
			net:       net,
		}, nil
	default:
		return nil, fmt.Errorf("expected *Network or string, got %T", entity)
	}
}

func (s *LeaseService) NetworkID() string {
	return s.net.ID
}

func (s *LeaseService) NetworkName() string {
	return s.net.Name
}

// GetLeases returns all IP leases for this network.
// Matches Python: returns new NetworkLeaseItem copies from db leases.
func (s *LeaseService) GetLeases(ctx context.Context) ([]*NetworkLeaseItem, error) {
	dbLeases, err := s.leaseRepo.ListAll(ctx, s.net.ID)
	if err != nil {
		return nil, err
	}
	result := make([]*NetworkLeaseItem, len(dbLeases))
	for i, l := range dbLeases {
		result[i] = &NetworkLeaseItem{
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
// Matches Python: returns NetworkLeaseItem copy or nil.
func (s *LeaseService) Get(ctx context.Context, ip string) (*NetworkLeaseItem, error) {
	lease, err := s.leaseRepo.Get(ctx, s.net.ID, ip)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, nil
	}
	return &NetworkLeaseItem{
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
func (s *LeaseService) GetByVMID(ctx context.Context, vmID string) ([]*NetworkLeaseItem, error) {
	dbLeases, err := s.leaseRepo.ListByVM(ctx, s.net.ID, vmID)
	if err != nil {
		return nil, err
	}
	result := make([]*NetworkLeaseItem, len(dbLeases))
	for i, l := range dbLeases {
		result[i] = &NetworkLeaseItem{
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

		allocatedIP, err := allocateNextIP(usedIPs, s.net.Subnet, s.net.IPv4Gateway)
		if err != nil {
			return "", errs.Wrap(errs.CodeNetworkLeaseExhausted, err)
		}

		vmIDCopy := vmID
		_, err = s.leaseRepo.Acquire(ctx, s.net.ID, allocatedIP, &vmIDCopy)
		if err == nil {
			return allocatedIP, nil
		}

		// Check for sqlite3.IntegrityError (UNIQUE constraint violation)
		if isSQLiteIntegrityError(err) {
			lastError = err
			continue
		}

		return "", err
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

// allocateNextIP finds the next available IP in a subnet, skipping gateway.
// Matches Python's NetworkUtils.allocate_next_ip exactly.
func allocateNextIP(existingIPs []string, subnet, gateway string) (string, error) {
	network := &net.IPNet{}
	if _, ipnet, err := net.ParseCIDR(subnet); err == nil {
		network = ipnet
	} else {
		return "", fmt.Errorf("invalid subnet: %s", subnet)
	}

	existingSet := make(map[string]bool)
	for _, ip := range existingIPs {
		existingSet[ip] = true
	}

	ip := network.IP.To4()
	mask := network.Mask
	ones, bits := mask.Size()
	total := 1 << (bits - ones)

	// Matches Python's ipaddress.IPv4Network(subnet, strict=False).hosts():
	// For /31 (RFC 3021): both addresses are usable.
	// For /32: the single address is usable.
	start := 1
	end := total - 1
	if total <= 2 {
		start = 0
		end = total
	}

	for i := start; i < end; i++ {
		n := ipToUint32(ip) + uint32(i)
		candidate := intToIP(n).String()

		if gateway != "" && candidate == gateway {
			continue
		}
		if !existingSet[candidate] {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no available IPs in subnet %s", subnet)
}

// ipToUint32 converts an IPv4 address to a uint32.
func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

// intToIP converts a uint32 to an IPv4 address.
func intToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

// isSQLiteIntegrityError checks if the error is a SQLite UNIQUE constraint violation.
// Matches Python's sqlite3.IntegrityError handling.
func isSQLiteIntegrityError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// sqlite3.IntegrityError manifests as "UNIQUE constraint failed: ..." in modernc.org/sqlite
	if strings.Contains(errStr, "UNIQUE constraint") {
		return true
	}
	if err == sql.ErrNoRows {
		return false
	}
	// Check for the specific error code
	return strings.Contains(errStr, "constraint failed")
}
