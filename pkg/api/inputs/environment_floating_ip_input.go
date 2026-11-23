package inputs

import (
	"context"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// EnvironmentFloatingIPOwnershipInput retains an exact reservation identity before creation.
// ResourceID is an idempotency token, not additional operator authority.
type EnvironmentFloatingIPOwnershipInput struct {
	ResourceID string `json:"resource_id" yaml:"resource_id"`
}

func (input EnvironmentFloatingIPOwnershipInput) Validate() error {
	if !environmentPolicyVMIDPattern.MatchString(input.ResourceID) {
		return errs.New(errs.CodeValidationFailed, "environment floating IP requires a full resource ID")
	}
	return nil
}

// ResolveOwnedNetwork binds a persisted environment reservation to its exact managed network.
func (input FloatingIPCreateInput) ResolveOwnedNetwork(
	ctx context.Context,
	repo network.Repository,
) (*model.NetworkItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if !environmentFloatingIPNetworkIDPattern.MatchString(input.Network) {
		return nil, errs.New(errs.CodeValidationFailed, "environment floating IP requires a full network ID")
	}
	item, err := repo.Get(ctx, input.Network)
	if err != nil {
		return nil, err
	}
	if item == nil || item.DeletedAt != nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "saved floating-IP network no longer exists")
	}
	if err := input.validateNetworkAddress(item); err != nil {
		return nil, err
	}
	return item, nil
}
