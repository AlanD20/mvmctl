package network

import (
	"context"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// Controller is the stateful network entity lifecycle manager.
type Controller struct {
	repo    Repository
	network *model.Network
}

// NewController creates a controller bound to a resolved network.
func NewController(network *model.Network, repo Repository) *Controller {
	return &Controller{repo: repo, network: network}
}

// Get returns the resolved network entity.
func (c *Controller) Get() *model.Network {
	return c.network
}

// SetDefault sets this network as the default.
func (c *Controller) SetDefault(ctx context.Context) error {
	if c.network == nil {
		return errs.NotFound(errs.CodeNetworkNotFound, "no network entity loaded")
	}
	return c.repo.SetDefault(ctx, c.network.ID)
}
