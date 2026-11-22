package api

import (
	"context"
	"maps"

	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

type VMLabelsAPI interface {
	VMLabelInspect(ctx context.Context, input inputs.VMLabelInput) (results.VMLabels, error)
	VMLabelUpdate(ctx context.Context, input inputs.VMLabelUpdateInput) (results.VMLabels, error)
}

func (op *Operation) VMLabelInspect(ctx context.Context, input inputs.VMLabelInput) (results.VMLabels, error) {
	vm, err := input.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return results.VMLabels{}, err
	}
	result, err := jailer.InspectVMLabels(ctx, jailer.VMLabelInspectRequest{VMID: vm.ID, CacheRoot: op.CacheDir})
	if err != nil {
		return results.VMLabels{}, err
	}
	return results.VMLabels{VMID: result.VMID, Revision: result.Revision, Labels: result.Labels}, nil
}

func (op *Operation) VMLabelUpdate(ctx context.Context, input inputs.VMLabelUpdateInput) (results.VMLabels, error) {
	if err := input.Validate(); err != nil {
		return results.VMLabels{}, err
	}
	vm, err := input.VMLabelInput.Resolve(ctx, op.Repos.VM)
	if err != nil {
		return results.VMLabels{}, err
	}
	labels := maps.Clone(input.Labels)
	result, err := jailer.UpdateVMLabels(ctx, jailer.VMLabelUpdateRequest{
		VMID: vm.ID, CacheRoot: op.CacheDir, ExpectedRevision: input.ExpectedRevision, Labels: labels,
	})
	if err != nil {
		return results.VMLabels{}, err
	}
	return results.VMLabels{VMID: result.VMID, Revision: result.Revision, Labels: labels}, nil
}
