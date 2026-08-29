// Package execpolicy owns directional VM-to-VM execution policy intent.
// Layer: Core domain — never imports other core/* packages.
package execpolicy

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository persists exact directional execution allow intent.
type Repository interface {
	Create(ctx context.Context, intent *model.ExecAllowIntent) error
	Get(ctx context.Context, intentID string) (*model.ExecAllowIntent, error)
	GetByIdentity(ctx context.Context, intent *model.ExecAllowIntent) (*model.ExecAllowIntent, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.ExecAllowIntent, error)
	List(ctx context.Context) ([]*model.ExecAllowIntent, error)
	DeleteMany(ctx context.Context, policyIDs []string) error
	DeleteByVM(ctx context.Context, vmID string) error
	Allows(ctx context.Context, sourceVMID, destinationVMID, targetUser string) (bool, error)
}
