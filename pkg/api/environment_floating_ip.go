package api

import (
	"context"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
)

// EnvironmentFloatingIPAPI retains a reservation identity before the root effect.
// The identity is an idempotency key, never additional operator authority.
type EnvironmentFloatingIPAPI interface {
	FloatingIPAPI
	FloatingIPDelegationEnsure(
		context.Context,
		inputs.FloatingIPDelegationEnsureInput,
	) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationGet(context.Context, inputs.FloatingIPDelegationInput) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationRemoveOwned(
		context.Context,
		inputs.FloatingIPDelegationRemoveOwnedInput,
	) (*model.FloatingIPDelegationItem, error)
	FloatingIPEnsure(
		context.Context,
		inputs.FloatingIPCreateInput,
		inputs.EnvironmentFloatingIPOwnershipInput,
	) (*model.FloatingIPItem, error)
}

// FloatingIPEnsure creates or recovers the exact reservation with matching intent.
func (op *Operation) FloatingIPEnsure(
	ctx context.Context,
	input inputs.FloatingIPCreateInput,
	ownership inputs.EnvironmentFloatingIPOwnershipInput,
) (*model.FloatingIPItem, error) {
	if err := ownership.Validate(); err != nil {
		return nil, err
	}
	item, err := input.Resolve(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	return op.Services.Network.CreateFloatingIP(ctx, ownership.ResourceID, item.ID, input.Name, input.IPv4)
}
