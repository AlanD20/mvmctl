// Package execpolicy owns directional VM-to-VM execution policy intent.
// Layer: Core domain — never imports other core/* packages.
package execpolicy

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository persists exact directional execution policies.
type Repository interface {
	Create(ctx context.Context, policy *model.ExecPolicy) error
	Get(ctx context.Context, policyID string) (*model.ExecPolicy, error)
	GetByIdentity(ctx context.Context, policy *model.ExecPolicy) (*model.ExecPolicy, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.ExecPolicy, error)
	List(ctx context.Context) ([]*model.ExecPolicy, error)
	DeleteMany(ctx context.Context, policyIDs []string) error
	DeleteByVM(ctx context.Context, vmID string) error
	Allows(ctx context.Context, sourceVMID, destinationVMID, targetUser string) (bool, error)
}
