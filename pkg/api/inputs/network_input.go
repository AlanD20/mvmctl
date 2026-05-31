package inputs

import (
	"context"
	"strings"

	"mvmctl/internal/core/network"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"

	"github.com/jmoiron/sqlx"
)

// NetworkInput is the raw input for identifying existing networks.
type NetworkInput struct {
	Identifiers []string `json:"identifiers"`
	Force       *bool    `json:"force,omitempty"`
}

// ResolvedNetworkInput matches Python's ResolvedNetworkInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedNetworkInput:
//	    networks: list[NetworkItem]
//	    force: bool | None = None
type ResolvedNetworkInput struct {
	Networks []*model.Network
	Force    *bool
}

// NetworkRequest matches Python's NetworkRequest.
//
// Resolve network identifiers to DB records and validate.
type NetworkRequest struct {
	db       *sqlx.DB
	input    NetworkInput
	result   *ResolvedNetworkInput
	resolver *network.Resolver
}

// NewNetworkRequest creates a new NetworkRequest.
// Python creates resolver with include=["leases"]:
//
//	self._network_resolver = Resolver(
//	    Repository(self._db), include=["leases"],
//	)
func NewNetworkRequest(inputs NetworkInput, db *sqlx.DB, networkRepo network.Repository) *NetworkRequest {
	return &NetworkRequest{
		db:       db,
		input:    inputs,
		resolver: network.NewResolver(networkRepo, []string{"leases"}),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves network identifiers to NetworkItem records.
// Matches Python's NetworkRequest.resolve().
func (r *NetworkRequest) Resolve(ctx context.Context) (*ResolvedNetworkInput, error) {
	if len(r.input.Identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "network",
			Message: "No network identifiers provided",
			Class:   errs.ClassValidation,
		}
	}

	result, err := r.resolver.ResolveMany(ctx, r.input.Identifiers)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "network",
			Message: err.Error(),
			Class:   errs.ClassValidation,
		}
	}

	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "network",
			Message: "Could not resolve any networks: " + strings.Join(result.Errors, ", "),
			Class:   errs.ClassValidation,
		}
	}

	r.result = &ResolvedNetworkInput{
		Networks: result.Items,
		Force:    r.input.Force,
	}

	// Validate
	if err := r.ensureValidate(); err != nil {
		return nil, err
	}

	return r.result, nil
}

func (r *NetworkRequest) ensureValidate() error {
	if r.result == nil {
		return &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "network",
			Message: "Failed to resolve necessary dependencies to validate",
			Class:   errs.ClassValidation,
		}
	}

	if len(r.result.Networks) == 0 {
		return &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "network",
			Message: "No networks found matching identifiers",
			Class:   errs.ClassValidation,
		}
	}

	return nil
}
