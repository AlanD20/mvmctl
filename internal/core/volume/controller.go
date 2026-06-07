package volume

import (
	"context"
	"fmt"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// Controller manages volume operations for a specific volume instance.
// Matches Python's VolumeController exactly — stateful: resolves entity eagerly
// in NewController and stores it internally.
type Controller struct {
	volume *model.VolumeItem
	repo   Repository
}

// NewController creates a new VolumeController for the given entity.
// If entity is a *Volume, it is used directly.
// If entity is a string (name or ID prefix), it is resolved via the resolver.
// Matches Python's VolumeController.__init__() exactly.
func NewController(ctx context.Context, entity any, repo Repository) (*Controller, error) {
	c := &Controller{repo: repo}
	switch e := entity.(type) {
	case *model.VolumeItem:
		c.volume = e
		return c, nil
	case string:
		resolver := NewResolver(repo)
		vol, err := resolver.Resolve(ctx, e)
		if err != nil {
			return nil, err
		}
		c.volume = vol
		return c, nil
	default:
		return nil, &errs.DomainError{
			Code:    errs.CodeVolumeNotFound,
			Op:      "volume",
			Message: fmt.Sprintf("Volume not found: '%v'", entity),
			Class:   errs.ClassValidation,
		}
	}
}

// Attach attaches the volume to a VM by updating its status and vm_id.
// Matches Python's VolumeController.attach() exactly — mutates c.volume.
func (c *Controller) Attach(ctx context.Context, vmID string) error {
	updated := &model.VolumeItem{
		ID:         c.volume.ID,
		Name:       c.volume.Name,
		SizeBytes:  c.volume.SizeBytes,
		Format:     c.volume.Format,
		Path:       c.volume.Path,
		Status:     model.VolumeStatusAttached,
		VMID:       &vmID,
		CreatedAt:  c.volume.CreatedAt,
		UpdatedAt:  c.volume.UpdatedAt,
		IsReadOnly: c.volume.IsReadOnly,
	}
	if err := c.repo.Upsert(ctx, updated); err != nil {
		return err
	}
	c.volume = updated
	return nil
}

// Detach detaches the volume from any VM by setting status to available and clearing vm_id.
// Matches Python's VolumeController.detach() exactly — mutates c.volume.
func (c *Controller) Detach(ctx context.Context) error {
	updated := &model.VolumeItem{
		ID:         c.volume.ID,
		Name:       c.volume.Name,
		SizeBytes:  c.volume.SizeBytes,
		Format:     c.volume.Format,
		Path:       c.volume.Path,
		Status:     model.VolumeStatusAvailable,
		VMID:       nil,
		CreatedAt:  c.volume.CreatedAt,
		UpdatedAt:  c.volume.UpdatedAt,
		IsReadOnly: c.volume.IsReadOnly,
	}
	if err := c.repo.Upsert(ctx, updated); err != nil {
		return err
	}
	c.volume = updated
	return nil
}
