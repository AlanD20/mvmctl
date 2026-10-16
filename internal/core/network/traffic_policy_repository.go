package network

import (
	"context"

	"mvmctl/internal/lib/model"
)

// TrafficPolicyRepository persists the two closed traffic allow-intent forms.
// InvalidateByVM spans both tables so callers cannot observe partial invalidation.
type TrafficPolicyRepository interface {
	CreateNetworkToVM(ctx context.Context, intent *model.NetworkToVMTrafficAllowIntent) error
	GetNetworkToVM(ctx context.Context, intentID string) (*model.NetworkToVMTrafficAllowIntent, error)
	GetNetworkByIdentity(
		ctx context.Context,
		intent *model.NetworkToVMTrafficAllowIntent,
	) (*model.NetworkToVMTrafficAllowIntent, error)
	FindNetworkToVMByPrefix(ctx context.Context, prefix string) ([]*model.NetworkToVMTrafficAllowIntent, error)
	ListNetworkToVM(ctx context.Context) ([]*model.NetworkToVMTrafficAllowIntent, error)
	DeleteNetworkToVM(ctx context.Context, intentIDs []string) error

	CreateVMToVM(ctx context.Context, intent *model.VMToVMTrafficAllowIntent) error
	GetVMToVM(ctx context.Context, intentID string) (*model.VMToVMTrafficAllowIntent, error)
	GetVMByIdentity(
		ctx context.Context,
		intent *model.VMToVMTrafficAllowIntent,
	) (*model.VMToVMTrafficAllowIntent, error)
	FindVMToVMByPrefix(ctx context.Context, prefix string) ([]*model.VMToVMTrafficAllowIntent, error)
	ListVMToVM(ctx context.Context) ([]*model.VMToVMTrafficAllowIntent, error)
	DeleteVMToVM(ctx context.Context, intentIDs []string) error

	InvalidateByVM(ctx context.Context, vmID string) error
	InvalidateByNetwork(ctx context.Context, networkID string) error
}
