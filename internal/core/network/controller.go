package network

import (
	"context"
	"database/sql"
	"fmt"

	"mvmctl/internal/infra/errs"
)

// Controller is the stateful network entity lifecycle manager.
// Matches src/mvmctl/core/network/_controller.py: Controller exactly.
type Controller struct {
	repo    Repository
	network *Network
}

// NewController creates a controller from a string identifier or *Network.
// Matches Python: Controller(entity: str | NetworkItem, repo)
// Python accepts both: isinstance(entity, NetworkItem) → use directly, else resolve.
// Note: Python's __init__ does NOT take a context — resolution happens synchronously.
func NewController(entity interface{}, repo Repository) (*Controller, error) {
	switch e := entity.(type) {
	case *Network:
		return &Controller{repo: repo, network: e}, nil
	case string:
		resolver := NewResolver(repo)
		net, err := resolver.Resolve(context.Background(), e)
		if err != nil {
			return nil, err
		}
		return &Controller{repo: repo, network: net}, nil
	default:
		return nil, fmt.Errorf("expected *Network or string, got %T", entity)
	}
}

// Get returns the resolved network entity.
func (c *Controller) Get() *Network {
	return c.network
}

// SetDefault sets this network as the default.
// Matches Python: calls self._repo.set_default(self._network.id)
func (c *Controller) SetDefault(ctx context.Context) error {
	if c.network == nil {
		return errs.NotFound(errs.CodeNetworkNotFound, "no network entity loaded")
	}
	return c.repo.SetDefault(ctx, c.network.ID)
}

// GetLeases returns all IP leases for this network.
// Matches Python: creates LeaseService with LeaseRepository(repo._db) and calls get_leases().
// Python accesses self._repo._db for the lease repo — Go receives db as a parameter.
func (c *Controller) GetLeases(ctx context.Context, db *sql.DB) ([]*NetworkLeaseItem, error) {
	if c.network == nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "no network entity loaded")
	}
	if db == nil {
		return nil, errs.ValidationFailed(errs.CodeDatabaseError, "no database connection available for lease lookup")
	}
	leaseRepo := NewLeaseRepository(db)
	leaseService, err := NewLeaseService(c.network, leaseRepo, nil)
	if err != nil {
		return nil, err
	}
	return leaseService.GetLeases(ctx)
}
