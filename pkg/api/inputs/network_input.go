package inputs

import (
	"context"
	"fmt"
	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
	"strings"
)

// NetworkInput is the raw input for identifying existing networks.
type NetworkInput struct {
	Identifiers []string `json:"identifiers"`
	Force       bool     `json:"force"`
}

// Validate checks that the network input has valid identifiers.
func (i *NetworkInput) Validate() error {
	if len(i.Identifiers) == 0 {
		return fmt.Errorf("at least one network identifier is required")
	}
	return nil
}

// Resolve resolves all identifiers in the input to NetworkItem objects.
// Delegates to network.Resolver.ResolveMany for batch resolution with
// deduplication and error collection.
func (i *NetworkInput) Resolve(ctx context.Context, repo network.Repository) ([]*model.NetworkItem, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	resolver := network.NewResolver(repo, []string{"leases"})
	result, err := resolver.ResolveMany(ctx, i.Identifiers)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkNotFound, err.Error())
	}
	if len(result.Items) == 0 {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "No networks found matching identifiers")
	}
	if len(result.Errors) > 0 {
		return result.Items, fmt.Errorf("partial resolve failures: %s", strings.Join(result.Errors, "; "))
	}
	return result.Items, nil
}
