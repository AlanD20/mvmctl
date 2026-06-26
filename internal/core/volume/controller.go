package volume

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Controller manages volume operations for a specific volume instance.
type Controller struct {
	volume *model.VolumeItem
	repo   Repository
}

// NewController creates a VolumeController bound to a resolved volume.
func NewController(volume *model.VolumeItem, repo Repository) *Controller {
	return &Controller{volume: volume, repo: repo}
}

// Attach attaches the volume to a VM by updating its status and vm_id.
// For shareable read-only volumes, the volume stays available and no VMID is set.
func (c *Controller) Attach(ctx context.Context, vmID string) error {
	if c.volume.IsShareable && c.volume.IsReadOnly {
		return nil
	}
	updated := &model.VolumeItem{
		ID:          c.volume.ID,
		Name:        c.volume.Name,
		SizeBytes:   c.volume.SizeBytes,
		Format:      c.volume.Format,
		Path:        c.volume.Path,
		Status:      model.VolumeStatusAttached,
		VMID:        &vmID,
		CreatedAt:   c.volume.CreatedAt,
		UpdatedAt:   c.volume.UpdatedAt,
		IsReadOnly:  c.volume.IsReadOnly,
		IsShareable: c.volume.IsShareable,
	}
	if err := c.repo.Upsert(ctx, updated); err != nil {
		return err
	}
	c.volume = updated
	return nil
}

// Detach detaches the volume from any VM by setting status to available and clearing vm_id.
// For shareable read-only volumes, detach is a no-op.
func (c *Controller) Detach(ctx context.Context) error {
	if c.volume.IsShareable && c.volume.IsReadOnly {
		return nil
	}
	updated := &model.VolumeItem{
		ID:          c.volume.ID,
		Name:        c.volume.Name,
		SizeBytes:   c.volume.SizeBytes,
		Format:      c.volume.Format,
		Path:        c.volume.Path,
		Status:      model.VolumeStatusAvailable,
		VMID:        nil,
		CreatedAt:   c.volume.CreatedAt,
		UpdatedAt:   c.volume.UpdatedAt,
		IsReadOnly:  c.volume.IsReadOnly,
		IsShareable: c.volume.IsShareable,
	}
	if err := c.repo.Upsert(ctx, updated); err != nil {
		return err
	}
	c.volume = updated
	return nil
}
