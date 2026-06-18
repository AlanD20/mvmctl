package network

import (
	"context"

	"mvmctl/internal/lib/model"
)

// LeaseRepository — database operations for network IP leases.
type LeaseRepository interface {
	Get(ctx context.Context, networkID string, ipv4 string) (*model.NetworkLeaseItem, error)
	ListAll(ctx context.Context, networkID string) ([]*model.NetworkLeaseItem, error)
	ListByVM(ctx context.Context, networkID string, vmID string) ([]*model.NetworkLeaseItem, error)
	ListAllBatch(ctx context.Context, networkIDs []string) ([]*model.NetworkLeaseItem, error)
	Acquire(ctx context.Context, networkID string, ipv4 string, vmID *string) (*model.NetworkLeaseItem, error)
	Release(ctx context.Context, networkID string, ipv4 string) error
	ReleaseByVM(ctx context.Context, vmID string) error
	Count(ctx context.Context) (int, error)
	CountAvailable(ctx context.Context, networkID string) (int, error)
}
