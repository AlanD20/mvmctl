package network

import "context"

// Repository — database operations for networks.
// Matches src/mvmctl/core/network/_repository.py: Repository
type Repository interface {
	Get(ctx context.Context, networkID string) (*Network, error)
	GetByName(ctx context.Context, name string) (*Network, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*Network, error)
	Count(ctx context.Context) (int, error)
	ListAll(ctx context.Context) ([]*Network, error)
	Upsert(ctx context.Context, network *Network) error
	UpdateBridgeActive(ctx context.Context, networkID string, active bool) error
	SetDefault(ctx context.Context, networkID string) error
	GetDefault(ctx context.Context) (*Network, error)
	UpdateManyIsPresent(ctx context.Context, networkIDs []string, isPresent bool) error
	SoftDelete(ctx context.Context, networkID string) error
	Delete(ctx context.Context, networkID string) error
}
