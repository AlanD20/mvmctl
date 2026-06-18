package inputs
import (
	"context"
	"strings"
	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
	"github.com/jmoiron/sqlx"
)
// NetworkInput is the raw input for identifying existing networks.
type NetworkInput struct {
	Identifiers []string `json:"identifiers"`
	Force       bool     `json:"force"`
}
// ResolvedNetworkInput specifies resolved network input.
type ResolvedNetworkInput struct {
	Networks []*model.NetworkItem
	Force    bool
}
// NetworkRequest specifies network request.
// Resolve network identifiers to DB records and validate.
type NetworkRequest struct {
	db       *sqlx.DB
	input    NetworkInput
	result   *ResolvedNetworkInput
	resolver *network.Resolver
}
// NewNetworkRequest creates a new NetworkRequest.
// Create resolver with lease enrichment.
func NewNetworkRequest(inputs NetworkInput, db *sqlx.DB, networkRepo network.Repository) *NetworkRequest {
	return &NetworkRequest{
		db:       db,
		input:    inputs,
		resolver: network.NewResolver(networkRepo, []string{"leases"}),
	}
}
// Result returns the resolved input, or nil if resolve() has not been called.
// Resolve resolves network identifiers to NetworkItem records.
func (r *NetworkRequest) Resolve(ctx context.Context) (*ResolvedNetworkInput, error) {
	if len(r.input.Identifiers) == 0 {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "No network identifiers provided")
	}
	result, err := r.resolver.ResolveMany(ctx, r.input.Identifiers)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkNotFound, err.Error())
	}
	if len(result.Errors) > 0 && len(result.Items) == 0 {
		return nil, errs.NotFound(
			errs.CodeNetworkNotFound,
			"Could not resolve any networks: "+strings.Join(result.Errors, ", "),
		)
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
		return errs.New(errs.CodeNetworkNotFound, "Failed to resolve necessary dependencies to validate")
	}
	if len(r.result.Networks) == 0 {
		return errs.NotFound(errs.CodeNetworkNotFound, "No networks found matching identifiers")
	}
	return nil
}
