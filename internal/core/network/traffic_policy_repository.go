package network

import (
	"context"

	"mvmctl/internal/lib/model"
)

// NetworkTrafficPolicyRepository persists network-source traffic intent.
type NetworkTrafficPolicyRepository interface {
	Create(ctx context.Context, policy *model.NetworkTrafficPolicy) error
	Get(ctx context.Context, policyID string) (*model.NetworkTrafficPolicy, error)
	GetByIdentity(ctx context.Context, policy *model.NetworkTrafficPolicy) (*model.NetworkTrafficPolicy, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.NetworkTrafficPolicy, error)
	List(ctx context.Context) ([]*model.NetworkTrafficPolicy, error)
	DeleteMany(ctx context.Context, policyIDs []string) error
	DeleteByVM(ctx context.Context, vmID string) error
	DeleteByNetwork(ctx context.Context, networkID string) error
}

// VMTrafficPolicyRepository persists exact-VM-source traffic intent.
type VMTrafficPolicyRepository interface {
	Create(ctx context.Context, policy *model.VMTrafficPolicy) error
	Get(ctx context.Context, policyID string) (*model.VMTrafficPolicy, error)
	GetByIdentity(ctx context.Context, policy *model.VMTrafficPolicy) (*model.VMTrafficPolicy, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.VMTrafficPolicy, error)
	List(ctx context.Context) ([]*model.VMTrafficPolicy, error)
	DeleteMany(ctx context.Context, policyIDs []string) error
	DeleteByVM(ctx context.Context, vmID string) error
}

// TrafficPolicyRepositories keeps the two closed source forms separate.
type TrafficPolicyRepositories struct {
	FromNetwork NetworkTrafficPolicyRepository
	FromVM      VMTrafficPolicyRepository
}
