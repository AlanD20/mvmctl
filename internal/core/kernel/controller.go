package kernel

import (
	"context"

	"mvmctl/internal/infra/model"
)

// Controller matches Python's Controller.
// Stateful kernel controller — bound to a single kernel instance.
type Controller struct {
	kernel *model.KernelItem
	repo   Repository
}

// NewController creates a Controller from an entity.
// entity can be a *model.KernelItem or a string identifier.
// Matches Python's Controller.__init__() which delegates string
// resolution to Resolver.resolve() for enrichment support.
func NewController(ctx context.Context, entity interface{}, repo Repository) (*Controller, error) {
	ctrl := &Controller{repo: repo}
	switch e := entity.(type) {
	case *model.KernelItem:
		ctrl.kernel = e
	case string:
		// Delegate to Resolver.Resolve() for full resolution logic,
		// matching Python's Controller which uses the resolver.
		r := NewResolver(repo, nil)
		k, err := r.Resolve(ctx, e)
		if err != nil {
			return nil, err
		}
		ctrl.kernel = k
	default:
		return nil, NewKernelError("invalid entity type")
	}
	return ctrl, nil
}

// Get returns the bound kernel item.
func (c *Controller) Get() *model.KernelItem {
	return c.kernel
}

// SetDefault sets this kernel as the default.
func (c *Controller) SetDefault(ctx context.Context) error {
	return c.repo.SetDefault(ctx, c.kernel.ID)
}
