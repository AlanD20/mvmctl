package network

import "context"

// LeaseRepository — database operations for network IP leases.
// Matches src/mvmctl/core/network/_repository.py: LeaseRepository
type LeaseRepository interface {
	Get(ctx context.Context, networkID string, ipv4 string) (*NetworkLeaseItem, error)
	ListAll(ctx context.Context, networkID string) ([]*NetworkLeaseItem, error)
	ListByVM(ctx context.Context, networkID string, vmID string) ([]*NetworkLeaseItem, error)
	ListAllBatch(ctx context.Context, networkIDs []string) ([]*NetworkLeaseItem, error)
	Acquire(ctx context.Context, networkID string, ipv4 string, vmID *string) (*NetworkLeaseItem, error)
	Release(ctx context.Context, networkID string, ipv4 string) error
	ReleaseByVM(ctx context.Context, vmID string) error
	Count(ctx context.Context) (int, error)
	CountAvailable(ctx context.Context, networkID string) (int, error)
}
