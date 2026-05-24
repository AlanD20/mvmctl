package volume

import (
	"context"

	"mvmctl/internal/infra/model"
)

// Repository defines all database operations for volumes.
// Matches Python's Repository exactly.
type Repository interface {
	// Get returns a volume by its full 64-char ID, or nil if not found.
	Get(ctx context.Context, id string) (*model.VolumeItem, error)

	// FindByPrefix returns all volumes whose ID starts with prefix.
	FindByPrefix(ctx context.Context, prefix string) ([]*model.VolumeItem, error)

	// GetByName returns a volume by its name, or nil if not found.
	GetByName(ctx context.Context, name string) (*model.VolumeItem, error)

	// ListAll returns all volumes ordered by created_at.
	// Matches Python's Repository.list_all().
	ListAll(ctx context.Context) ([]*model.VolumeItem, error)

	// Upsert inserts or replaces a volume record using INSERT ... ON CONFLICT(id) DO UPDATE.
	Upsert(ctx context.Context, volume *model.VolumeItem) error

	// Delete removes a volume by ID. No-op if not found.
	Delete(ctx context.Context, id string) error

	// FindByIDs returns all volumes matching the given IDs.
	FindByIDs(ctx context.Context, ids []string) ([]*model.VolumeItem, error)

	// Count returns the total number of volumes.
	Count(ctx context.Context) (int, error)
}
