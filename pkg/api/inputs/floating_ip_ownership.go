package inputs

import (
	"mvmctl/pkg/errs"
	"strings"
)

// FloatingIPAssociateInput requests persistent ownership for an existing eligible VM.
// A zero ExpectedGeneration selects a fresh root observation before compare-and-swap.
type FloatingIPAssociateInput struct {
	Network            string `json:"network"`
	Identifier         string `json:"identifier"`
	VM                 string `json:"vm"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

// Validate admits bounded selectors without trusting them as identity authority.
func (input FloatingIPAssociateInput) Validate() error {
	if err := (FloatingIPInput{Network: input.Network, Identifier: input.Identifier}).Validate(); err != nil {
		return err
	}
	if input.VM == "" || strings.TrimSpace(input.VM) != input.VM || len(input.VM) > 255 {
		return errs.New(errs.CodeValidationFailed, "floating-IP ownership requires a VM selector")
	}
	return nil
}

// FloatingIPTransferInput explicitly authorizes replacement of current ownership.
type FloatingIPTransferInput struct {
	Network            string `json:"network"`
	Identifier         string `json:"identifier"`
	VM                 string `json:"vm"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

// Validate requires an existing-resource and target selector.
func (input FloatingIPTransferInput) Validate() error {
	return (FloatingIPAssociateInput{Network: input.Network, Identifier: input.Identifier, VM: input.VM, ExpectedGeneration: input.ExpectedGeneration}).Validate()
}

// FloatingIPReleaseInput revokes the observed resource ownership.
type FloatingIPReleaseInput struct {
	Network            string `json:"network"`
	Identifier         string `json:"identifier"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

// Validate requires a bounded resource selector.
func (input FloatingIPReleaseInput) Validate() error {
	return (FloatingIPInput{Network: input.Network, Identifier: input.Identifier}).Validate()
}
