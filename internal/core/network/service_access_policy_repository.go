package network

import (
	"context"

	"mvmctl/internal/lib/model"
)

// ServiceAccessPolicyRepository persists and resolves typed service-access policies.
type ServiceAccessPolicyRepository interface {
	Create(ctx context.Context, policy *model.ServiceAccessPolicy) error
	Get(ctx context.Context, policyID string) (*model.ServiceAccessPolicy, error)
	GetByIdentity(ctx context.Context, policy *model.ServiceAccessPolicy) (*model.ServiceAccessPolicy, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.ServiceAccessPolicy, error)
	List(ctx context.Context) ([]*model.ServiceAccessPolicy, error)
	Delete(ctx context.Context, policyID string) error
	DeleteMany(ctx context.Context, policyIDs []string) error
	DeleteByVM(ctx context.Context, vmID string) error
	DeleteBySourceNetwork(ctx context.Context, networkID string) error
}
