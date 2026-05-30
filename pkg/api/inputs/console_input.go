package inputs

import (
	"context"
	"database/sql"

	"mvmctl/internal/infra/model"
)

// ConsoleInput matches Python's ConsoleInput dataclass.
//
//	@dataclass
//	class ConsoleInput:
//	    identifier: str
type ConsoleInput struct {
	Identifier string `json:"identifier"`
}

// ResolvedConsoleInput matches Python's ResolvedConsoleInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedConsoleInput:
//	    vm: VMInstanceItem
//	    relay: ConsoleRelayManager
type ResolvedConsoleInput struct {
	VM    *model.VM
	Relay model.ConsoleRelay
}

// ConsoleRequest matches Python's ConsoleRequest.
//
// Resolve the VM for console operations.
type ConsoleRequest struct {
	db     *sql.DB
	input  ConsoleInput
	result *ResolvedConsoleInput
}

// NewConsoleRequest creates a new ConsoleRequest.
func NewConsoleRequest(inputs ConsoleInput, db *sql.DB) *ConsoleRequest {
	return &ConsoleRequest{
		db:    db,
		input: inputs,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve stores the resolved VM and relay in the request result.
// Matches Python's ConsoleRequest.resolve().
// The relay is created by the caller (API layer) to avoid importing
// internal/service/console from this package.
func (r *ConsoleRequest) Resolve(ctx context.Context, vmEntity *model.VM, relay model.ConsoleRelay) (*ResolvedConsoleInput, error) {
	r.result = &ResolvedConsoleInput{
		VM:    vmEntity,
		Relay: relay,
	}
	return r.result, nil
}
