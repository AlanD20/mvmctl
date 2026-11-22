package inputs

import (
	"context"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

type VMLabelInput struct {
	VM string `json:"vm"`
}

func (input VMLabelInput) Validate() error {
	vm := VMInput{Identifiers: []string{input.VM}}
	return vm.Validate()
}

func (input VMLabelInput) Resolve(ctx context.Context, repo vm.Repository) (*model.VMItem, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	return vm.NewResolver(repo).Resolve(ctx, input.VM)
}

type VMLabelUpdateInput struct {
	VMLabelInput
	ExpectedRevision uint64         `json:"expected_revision"`
	Labels           model.VMLabels `json:"labels"`
}

func (input VMLabelUpdateInput) Validate() error {
	if err := input.VMLabelInput.Validate(); err != nil {
		return err
	}
	if input.Labels == nil {
		return errs.New(errs.CodeValidationFailed, "VM label update requires an explicit map")
	}
	return input.Labels.Validate()
}
