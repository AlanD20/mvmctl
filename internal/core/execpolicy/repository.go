// Package execpolicy owns directional VM-to-VM execution policy intent.
// Layer: Core domain — never imports other core/* packages.
package execpolicy

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository persists exact directional execution allow intent.
type Repository interface {
	Create(ctx context.Context, intent *model.VMToVMExecAllowIntent) error
	Get(ctx context.Context, intentID string) (*model.VMToVMExecAllowIntent, error)
	GetByIdentity(ctx context.Context, intent *model.VMToVMExecAllowIntent) (*model.VMToVMExecAllowIntent, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.VMToVMExecAllowIntent, error)
	List(ctx context.Context) ([]*model.VMToVMExecAllowIntent, error)
	DeleteMany(ctx context.Context, policyIDs []string) error
	DeleteByVM(ctx context.Context, vmID string) error
	Allows(ctx context.Context, sourceVMID, destinationVMID, targetUser string) (bool, error)
}
