package inputs

import (
	"fmt"
	"mvmctl/internal/lib/model"
)

// ConsoleInput specifies console input.
type ConsoleInput struct {
	Identifier string `json:"identifier"`
}

// ResolvedConsoleInput specifies resolved console input.
type ResolvedConsoleInput struct {
	VM    *model.VMItem
	Relay model.ConsoleRelay
}

// Validate checks that the console input has a VM identifier.
func (i *ConsoleInput) Validate() error {
	if i.Identifier == "" {
		return fmt.Errorf("VM identifier is required for console operations")
	}
	return nil
}

// Resolve stores the resolved VM and relay in the resolved input.
// The relay is created by the caller (API layer) to avoid importing
// internal/service/console from this package.
func (i *ConsoleInput) Resolve(vmEntity *model.VMItem, relay model.ConsoleRelay) (*ResolvedConsoleInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return &ResolvedConsoleInput{
		VM:    vmEntity,
		Relay: relay,
	}, nil
}
