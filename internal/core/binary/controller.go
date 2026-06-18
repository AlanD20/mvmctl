// Package binary provides Firecracker/jailer binary version management.
// Layer: Core domain — never imports other core/* packages.
package binary

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Controller binds a resolved binary item to its repository for state management.
type Controller struct {
	binary *model.BinaryItem
	repo   Repository
}

// NewController creates a BinaryController bound to a resolved binary.
func NewController(binary *model.BinaryItem, repo Repository) *Controller {
	return &Controller{binary: binary, repo: repo}
}

// Get returns the resolved binary.
func (c *Controller) Get() *model.BinaryItem {
	return c.binary
}

// SetDefault sets this binary as default (clears others with same name).
func (c *Controller) SetDefault(ctx context.Context) error {
	return c.repo.SetDefault(ctx, c.binary.Type, c.binary.Version, c.binary.Path)
}
