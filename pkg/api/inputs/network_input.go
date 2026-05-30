package inputs

import (
	"context"
	"database/sql"
	"strings"

	"mvmctl/internal/core/network"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// NetworkInput is the raw input for identifying existing networks.
// Matches Python's NetworkInput dataclass:
//
//	@dataclass
//	class NetworkInput:
//	    name: list[str] = field(default_factory=list)
//	    id: list[str] = field(default_factory=list)
//	    force: bool | None = None
type NetworkInput struct {
	Name  []string `json:"name,omitempty"`
	ID    []string `json:"id,omitempty"`
	Force *bool    `json:"force,omitempty"`
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
	db       *sql.DB
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
func NewNetworkRequest(inputs NetworkInput, db *sql.DB, networkRepo network.Repository) *NetworkRequest {
	return &NetworkRequest{
		db:       db,
		input:    inputs,
		resolver: network.NewResolverWithInclude(networkRepo, []string{"leases"}),
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// Resolve resolves network identifiers to NetworkItem records.
// Matches Python's NetworkRequest.resolve().
func (r *NetworkRequest) Resolve(ctx context.Context) (*ResolvedNetworkInput, error) {
	identifiers := append(r.input.Name, r.input.ID...)

	if len(identifiers) == 0 {
		return nil, &errs.DomainError{
			Code:    errs.CodeNetworkNotFound,
			Op:      "network",
			Message: "No network identifiers provided",
			Class:   errs.ClassValidation,
		}
	}

	result, err := r.resolver.ResolveMany(ctx, identifiers)
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
