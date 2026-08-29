package network

import (
	"context"

	"mvmctl/internal/lib/model"
)

// TrafficPolicyRepository persists the two closed traffic allow-intent forms.
// InvalidateByVM spans both tables so callers cannot observe partial invalidation.
type TrafficPolicyRepository interface {
	CreateFromNetwork(ctx context.Context, intent *model.NetworkTrafficAllowIntent) error
	GetFromNetwork(ctx context.Context, intentID string) (*model.NetworkTrafficAllowIntent, error)
	GetNetworkByIdentity(
		ctx context.Context,
		intent *model.NetworkTrafficAllowIntent,
	) (*model.NetworkTrafficAllowIntent, error)
	FindNetworksByPrefix(ctx context.Context, prefix string) ([]*model.NetworkTrafficAllowIntent, error)
	ListFromNetworks(ctx context.Context) ([]*model.NetworkTrafficAllowIntent, error)
	DeleteFromNetworks(ctx context.Context, intentIDs []string) error

	CreateFromVM(ctx context.Context, intent *model.VMTrafficAllowIntent) error
	GetFromVM(ctx context.Context, intentID string) (*model.VMTrafficAllowIntent, error)
	GetVMByIdentity(ctx context.Context, intent *model.VMTrafficAllowIntent) (*model.VMTrafficAllowIntent, error)
	FindVMsByPrefix(ctx context.Context, prefix string) ([]*model.VMTrafficAllowIntent, error)
	ListFromVMs(ctx context.Context) ([]*model.VMTrafficAllowIntent, error)
	DeleteFromVMs(ctx context.Context, intentIDs []string) error

	InvalidateByVM(ctx context.Context, vmID string) error
	InvalidateByNetwork(ctx context.Context, networkID string) error
}
