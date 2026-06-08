package kernel

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Controller matches Python's Controller.
type Controller struct {
	kernel *model.KernelItem
	repo   Repository
}

// NewController creates a Controller bound to a resolved kernel.
func NewController(kernel *model.KernelItem, repo Repository) *Controller {
	return &Controller{kernel: kernel, repo: repo}
}

// Get returns the bound kernel item.
func (c *Controller) Get() *model.KernelItem {
	return c.kernel
}

// SetDefault sets this kernel as the default.
func (c *Controller) SetDefault(ctx context.Context) error {
	return c.repo.SetDefault(ctx, c.kernel.ID)
}
