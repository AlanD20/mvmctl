package api

import (
	"context"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// FloatingIPOwnershipAPI exposes named operator authority independently of guest delegation.
type FloatingIPOwnershipAPI interface {
	FloatingIPAssociate(context.Context, inputs.FloatingIPAssociateInput) (*model.FloatingIPItem, error)
	FloatingIPTransfer(context.Context, inputs.FloatingIPTransferInput) (*model.FloatingIPItem, error)
	FloatingIPRelease(context.Context, inputs.FloatingIPReleaseInput) (*model.FloatingIPItem, error)
}

func (op *Operation) FloatingIPAssociate(
	ctx context.Context,
	input inputs.FloatingIPAssociateInput,
) (*model.FloatingIPItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	resource, target, err := op.resolveFloatingIPOwner(ctx, input)
	if err != nil {
		return nil, err
	}
	result, err := op.Services.Network.AssociateFloatingIP(
		ctx,
		resource,
		target.ID,
		floatingIPExpectedGeneration(resource, input.ExpectedGeneration),
	)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation(
		"floating_ip.associate",
		map[string]any{"id": result.ID, "vm_id": target.ID, "generation": result.Generation},
		"",
	)
	return result, nil
}

func (op *Operation) FloatingIPTransfer(
	ctx context.Context,
	input inputs.FloatingIPTransferInput,
) (*model.FloatingIPItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	resource, target, err := op.resolveFloatingIPOwner(
		ctx,
		inputs.FloatingIPAssociateInput{
			Network:            input.Network,
			Identifier:         input.Identifier,
			VM:                 input.VM,
			ExpectedGeneration: input.ExpectedGeneration,
		},
	)
	if err != nil {
		return nil, err
	}
	result, err := op.Services.Network.TransferFloatingIP(
		ctx,
		resource,
		target.ID,
		floatingIPExpectedGeneration(resource, input.ExpectedGeneration),
	)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation(
		"floating_ip.transfer",
		map[string]any{"id": result.ID, "vm_id": target.ID, "generation": result.Generation},
		"",
	)
	return result, nil
}

func (op *Operation) FloatingIPRelease(
	ctx context.Context,
	input inputs.FloatingIPReleaseInput,
) (*model.FloatingIPItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	resource, err := op.FloatingIPGet(ctx, inputs.FloatingIPInput{Network: input.Network, Identifier: input.Identifier})
	if err != nil {
		return nil, err
	}
	result, err := op.Services.Network.ReleaseFloatingIP(
		ctx,
		resource,
		floatingIPExpectedGeneration(resource, input.ExpectedGeneration),
	)
	if err != nil {
		return nil, err
	}
	op.AuditLog.LogOperation(
		"floating_ip.release",
		map[string]any{"id": result.ID, "generation": result.Generation},
		"",
	)
	return result, nil
}

func (op *Operation) resolveFloatingIPOwner(
	ctx context.Context,
	input inputs.FloatingIPAssociateInput,
) (*model.FloatingIPItem, *model.VMItem, error) {
	resource, err := op.FloatingIPGet(ctx, inputs.FloatingIPInput{Network: input.Network, Identifier: input.Identifier})
	if err != nil {
		return nil, nil, err
	}
	target, err := vm.NewResolver(op.Repos.VM).Resolve(ctx, input.VM)
	if err != nil {
		return nil, nil, err
	}
	if string(target.NetworkID) != resource.NetworkID {
		return nil, nil, errs.New(errs.CodeValidationFailed, "ownership VM must belong to the floating-IP network")
	}
	return resource, target, nil
}
func floatingIPExpectedGeneration(resource *model.FloatingIPItem, expected uint64) uint64 {
	if expected != 0 {
		return expected
	}
	return resource.Generation
}

// FloatingIPInspect resolves identity before obtaining a fresh root packet-authority observation.
func (op *Operation) FloatingIPInspect(
	ctx context.Context,
	input inputs.FloatingIPInput,
) (*model.FloatingIPItem, error) {
	resource, err := op.FloatingIPGet(ctx, input)
	if err != nil {
		return nil, err
	}
	return op.Services.Network.InspectFloatingIP(ctx, resource)
}
