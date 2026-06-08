package network

import (
	"context"
	"fmt"

	"mvmctl/internal/infra/model"
	"mvmctl/pkg/errs"
)

// Controller is the stateful network entity lifecycle manager.
// Matches src/mvmctl/core/network/_controller.py: Controller exactly.
type Controller struct {
	repo    Repository
	network *model.Network
}

// NewController creates a controller from a string identifier or *model.Network.
func NewController(ctx context.Context, entity any, repo Repository) (*Controller, error) {
	switch e := entity.(type) {
	case *model.Network:
		return &Controller{repo: repo, network: e}, nil
	case string:
		resolver := NewResolver(repo, nil)
		net, err := resolver.Resolve(ctx, e)
		if err != nil {
			return nil, err
		}
		return &Controller{repo: repo, network: net}, nil
	default:
		return nil, fmt.Errorf("expected *model.Network or string, got %T", entity)
	}
}

// Get returns the resolved network entity.
func (c *Controller) Get() *model.Network {
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
