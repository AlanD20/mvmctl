package api

import (
	"context"
	"strings"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// FloatingIPDelegationAPI administers stored root grants separately from guest session readiness.
type FloatingIPDelegationAPI interface {
	FloatingIPDelegationEnsure(context.Context,
		inputs.FloatingIPDelegationEnsureInput) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationRemoveOwned(context.Context,
		inputs.FloatingIPDelegationRemoveOwnedInput) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationGrant(context.Context,
		inputs.FloatingIPDelegationGrantInput) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationList(context.Context, inputs.FloatingIPInput) ([]*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationGet(context.Context, inputs.FloatingIPDelegationInput) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationRevoke(
		context.Context,
		inputs.FloatingIPDelegationInput,
	) (*model.FloatingIPDelegationItem, error)
}

func (op *Operation) FloatingIPDelegationGrant(
	ctx context.Context, input inputs.FloatingIPDelegationGrantInput,
) (*model.FloatingIPDelegationItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	resource, err := op.FloatingIPGet(ctx, inputs.FloatingIPInput{Network: input.Network, Identifier: input.Resource})
	if err != nil {
		return nil, err
	}
	target, err := vm.NewResolver(op.Repos.VM).Resolve(ctx, input.VM)
	if err != nil {
		return nil, err
	}
	input.Network, input.Resource, input.VM = resource.NetworkID, resource.ID, target.ID
	return op.FloatingIPDelegationEnsure(ctx, inputs.FloatingIPDelegationEnsureInput{
		FloatingIPDelegationGrantInput: input, ID: strings.ReplaceAll(crypto.UUIDV4(), "-", "")})
}

// FloatingIPDelegationEnsure uses a persisted exact creation identity for crash-safe immutable-intent retries.
func (op *Operation) FloatingIPDelegationEnsure(
	ctx context.Context, input inputs.FloatingIPDelegationEnsureInput,
) (*model.FloatingIPDelegationItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	network, err := op.Repos.Network.Get(ctx, input.Network)
	if err != nil {
		return nil, err
	}
	if network == nil || network.DeletedAt != nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "saved delegation network no longer exists")
	}
	target, err := op.Repos.VM.Get(ctx, input.VM)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, "saved delegation VM no longer exists")
	}
	if string(target.NetworkID) != input.Network {
		return nil, errs.New(errs.CodeValidationFailed, "delegation VM must belong to the floating-IP network")
	}
	duration, err := input.LeaseDuration()
	if err != nil {
		return nil, err
	}
	result, err := op.floatingIPAuthority.GrantFloatingIPDelegation(ctx, input.ID, input.Network, input.Resource,
		input.VM, input.Method, duration)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation("floating_ip.delegation.grant", map[string]any{
		"id": result.ID, "resource_id": result.ResourceID, "vm_id": result.VMID, "method": result.Method}, "")
	return result, nil
}

func (op *Operation) FloatingIPDelegationList(
	ctx context.Context, input inputs.FloatingIPInput,
) ([]*model.FloatingIPDelegationItem, error) {
	resource, err := op.FloatingIPGet(ctx, input)
	if err != nil {
		return nil, err
	}
	return op.Services.Network.ListFloatingIPDelegations(ctx, resource.NetworkID, resource.ID)
}

func (op *Operation) FloatingIPDelegationGet(
	ctx context.Context, input inputs.FloatingIPDelegationInput,
) (*model.FloatingIPDelegationItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	network, err := (inputs.FloatingIPNetworkInput{Network: input.Network}).Resolve(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	return op.Services.Network.GetFloatingIPDelegation(ctx, network.ID, input.ID)
}

func (op *Operation) FloatingIPDelegationRevoke(
	ctx context.Context, input inputs.FloatingIPDelegationInput,
) (*model.FloatingIPDelegationItem, error) {
	item, err := op.FloatingIPDelegationGet(ctx, input)
	if err != nil {
		return nil, err
	}
	result, err := op.Services.Network.RevokeFloatingIPDelegation(ctx, item)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation("floating_ip.delegation.revoke", map[string]any{
		"id": result.ID, "resource_id": result.ResourceID, "revision": result.Revision}, "")
	return result, nil
}

// FloatingIPDelegationRemoveOwned resumes cleanup only for the exact root-observed grant lifetime.
func (op *Operation) FloatingIPDelegationRemoveOwned(
	ctx context.Context, input inputs.FloatingIPDelegationRemoveOwnedInput,
) (*model.FloatingIPDelegationItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	result, err := op.Services.Network.RemoveOwnedFloatingIPDelegation(ctx, input.Grant)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation("floating_ip.delegation.remove_owned", map[string]any{
		"id": result.ID, "resource_id": result.ResourceID, "revision": result.Revision}, "")
	return result, nil
}
