package image

import (
	"context"

	"mvmctl/internal/infra/model"
)

// Repository matches Python's Repository class methods exactly.
type Repository interface {
	// Get returns an image by its full 64-char ID, or nil if not found.
	Get(ctx context.Context, imageID string) (*model.ImageItem, error)
	// FindByPrefix returns all images whose ID starts with prefix.
	FindByPrefix(ctx context.Context, prefix string) ([]*model.ImageItem, error)
	// GetByType returns an image by its type, preferring the default, or nil.
	GetByType(ctx context.Context, imgType string) (*model.ImageItem, error)
	// GetByVersionAndType returns an image by version and type, or nil.
	GetByVersionAndType(ctx context.Context, version, imgType string) (*model.ImageItem, error)
	// GetByName returns an image by its display name, or nil.
	GetByName(ctx context.Context, name string) (*model.ImageItem, error)
	// Count returns total count of all non-deleted images.
	Count(ctx context.Context) (int, error)
	// ListAll returns all non-deleted images ordered by created_at.
	ListAll(ctx context.Context) ([]*model.ImageItem, error)
	// Upsert inserts or replaces an image record.
	Upsert(ctx context.Context, img *model.ImageItem) error
	// SoftDelete sets deleted_at and is_present=0.
	SoftDelete(ctx context.Context, imageID string) error
	// Delete removes an image record permanently.
	Delete(ctx context.Context, imageID string) error
	// SetDefault sets one image as default, clearing all others atomically.
	SetDefault(ctx context.Context, imageID string) error
	// GetDefault returns the default image, or nil if not set.
	GetDefault(ctx context.Context) (*model.ImageItem, error)
	// UpdateManyIsPresent bulk-updates the is_present flag.
	UpdateManyIsPresent(ctx context.Context, imageIDs []string, isPresent bool) error
}
