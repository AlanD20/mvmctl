package binary

import (
	"context"

	"mvmctl/internal/infra/model"
	"mvmctl/pkg/errs"
)

// Controller matches Python's BinaryController.
// Stateful binary manager — resolves binary entity in NewController and
// operates on cached BinaryItem.
type Controller struct {
	binary *model.BinaryItem
	repo   Repository
}

// NewController creates a BinaryController from an entity.
// entity can be a *model.BinaryItem or a string identifier.
// Resolves the binary eagerly at construction time (like Python).
func NewController(ctx context.Context, entity any, repo Repository) (*Controller, error) {
	var b *model.BinaryItem
	switch e := entity.(type) {
	case *model.BinaryItem:
		b = e
	case string:
		resolver := NewResolver(repo)
		var err error
		b, err = resolver.Resolve(ctx, e)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errs.New(errs.CodeInternal, "invalid entity type")
	}
	return &Controller{binary: b, repo: repo}, nil
}

// Get returns the resolved binary.
func (c *Controller) Get() *model.BinaryItem {
	return c.binary
}

// SetDefault sets this binary as default (clears others with same name).
func (c *Controller) SetDefault(ctx context.Context) error {
	return c.repo.SetDefault(ctx, c.binary.Name, c.binary.Version, c.binary.Path)
}
