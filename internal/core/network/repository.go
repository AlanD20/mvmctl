// Package network provides TAP/bridge network interface management.
// Layer: Core domain — never imports other core/* packages.
package network

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository — database operations for networks.
type Repository interface {
	Get(ctx context.Context, networkID string) (*model.Network, error)
	GetByName(ctx context.Context, name string) (*model.Network, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.Network, error)
	Count(ctx context.Context) (int, error)
	ListAll(ctx context.Context) ([]*model.Network, error)
	Upsert(ctx context.Context, network *model.Network) error
	UpdateBridgeActive(ctx context.Context, networkID string, active bool) error
	SetDefault(ctx context.Context, networkID string) error
	GetDefault(ctx context.Context) (*model.Network, error)
	UpdateManyIsPresent(ctx context.Context, networkIDs []string, isPresent bool) error
	SoftDelete(ctx context.Context, networkID string) error
	Delete(ctx context.Context, networkID string) error
}
