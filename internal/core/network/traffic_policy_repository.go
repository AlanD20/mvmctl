package network

import (
	"context"

	"mvmctl/internal/lib/model"
)

// TrafficPolicyRepository persists closed VM and managed-host traffic allow intents.
// InvalidateByVM spans all affected tables so callers cannot observe partial invalidation.
type TrafficPolicyRepository interface {
	ProjectVMToFloatingIP(context.Context, *model.VMToFloatingIPTrafficAllowIntent) error
	GetVMToFloatingIP(context.Context, string) (*model.VMToFloatingIPTrafficAllowIntent, error)
	FindVMToFloatingIPByPrefix(context.Context, string) ([]*model.VMToFloatingIPTrafficAllowIntent, error)
	ListVMToFloatingIP(context.Context) ([]*model.VMToFloatingIPTrafficAllowIntent, error)
	DeleteOwnedVMToFloatingIP(ctx context.Context, policyID, claim, incarnation string) error
	ProjectNetworkToFloatingIP(context.Context, *model.NetworkToFloatingIPTrafficAllowIntent) error
	GetNetworkToFloatingIP(context.Context, string) (*model.NetworkToFloatingIPTrafficAllowIntent, error)
	FindNetworkToFloatingIPByPrefix(context.Context, string) ([]*model.NetworkToFloatingIPTrafficAllowIntent, error)
	ListNetworkToFloatingIP(context.Context) ([]*model.NetworkToFloatingIPTrafficAllowIntent, error)
	DeleteOwnedNetworkToFloatingIP(ctx context.Context, policyID, claim, incarnation string) error
	InvalidateByFloatingIP(context.Context, string) error

	ProjectVMToHost(ctx context.Context, intent *model.VMToHostTrafficAllowIntent) error
	ProjectNetworkToHost(ctx context.Context, intent *model.NetworkToHostTrafficAllowIntent) error
	GetVMToHost(ctx context.Context, policyID string) (*model.VMToHostTrafficAllowIntent, error)
	GetNetworkToHost(ctx context.Context, policyID string) (*model.NetworkToHostTrafficAllowIntent, error)
	FindVMToHostByPrefix(ctx context.Context, prefix string) ([]*model.VMToHostTrafficAllowIntent, error)
	FindNetworkToHostByPrefix(ctx context.Context, prefix string) ([]*model.NetworkToHostTrafficAllowIntent, error)
	ListVMToHost(ctx context.Context) ([]*model.VMToHostTrafficAllowIntent, error)
	ListNetworkToHost(ctx context.Context) ([]*model.NetworkToHostTrafficAllowIntent, error)
	DeleteVMToHost(ctx context.Context, policyIDs []string) error
	DeleteNetworkToHost(ctx context.Context, policyIDs []string) error
	DeleteOwnedVMToHost(ctx context.Context, policyID, claim, incarnation string) error
	DeleteOwnedNetworkToHost(ctx context.Context, policyID, claim, incarnation string) error
	ProjectNetworkToVM(ctx context.Context, intent *model.NetworkToVMTrafficAllowIntent) error
	ProjectVMToVM(ctx context.Context, intent *model.VMToVMTrafficAllowIntent) error
	DeleteOwnedNetworkToVM(ctx context.Context, policyID, claim, incarnation string) error
	DeleteOwnedVMToVM(ctx context.Context, policyID, claim, incarnation string) error
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
