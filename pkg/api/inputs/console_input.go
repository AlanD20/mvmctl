package inputs
import (
	"context"
	"mvmctl/internal/lib/model"
	"github.com/jmoiron/sqlx"
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
// ConsoleRequest specifies console request.
// Resolve the VM for console operations.
type ConsoleRequest struct {
	db     *sqlx.DB
	input  ConsoleInput
	result *ResolvedConsoleInput
}
// NewConsoleRequest creates a new ConsoleRequest.
func NewConsoleRequest(inputs ConsoleInput, db *sqlx.DB) *ConsoleRequest {
	return &ConsoleRequest{
		db:    db,
		input: inputs,
	}
}
// Result returns the resolved input, or nil if resolve() has not been called.
// Resolve stores the resolved VM and relay in the request result.
// The relay is created by the caller (API layer) to avoid importing
// internal/service/console from this package.
func (r *ConsoleRequest) Resolve(
	ctx context.Context,
	vmEntity *model.VMItem,
	relay model.ConsoleRelay,
) (*ResolvedConsoleInput, error) {
	r.result = &ResolvedConsoleInput{
		VM:    vmEntity,
		Relay: relay,
	}
	return r.result, nil
}
