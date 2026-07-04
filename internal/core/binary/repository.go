package binary

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository defines the persistence contract for binary items.
type Repository interface {
	// Get returns a binary by its full 64-char ID, or nil if not found.
	Get(ctx context.Context, id string) (*model.BinaryItem, error)
	// FindByPrefix returns all binaries whose ID starts with prefix.
	FindByPrefix(ctx context.Context, prefix string, includeDeleted ...bool) ([]*model.BinaryItem, error)
	// ListAll returns all non-deleted binaries ordered by created_at.
	ListAll(ctx context.Context) ([]*model.BinaryItem, error)
	// ListByType returns all binaries with a given type.
	ListByType(ctx context.Context, typ string) ([]*model.BinaryItem, error)
	// GetByTypeAndVersion returns a binary by type and version, or nil.
	GetByTypeAndVersion(ctx context.Context, typ, version string) (*model.BinaryItem, error)
	// Upsert inserts or replaces a binary record.
	Upsert(ctx context.Context, binary *model.BinaryItem) error
	// Delete hard-deletes a binary by ID.
	Delete(ctx context.Context, id string) error
	// DeleteByType deletes ALL binary rows matching the given type.
	DeleteByType(ctx context.Context, typ string) error
	// DeleteByTypeAndVersion deletes the binary row matching type AND version.
	DeleteByTypeAndVersion(ctx context.Context, typ, version string) error
	// SetDefault sets a binary as default, clearing all others with the same type.
	SetDefault(ctx context.Context, typ, id string) error
	// Count returns total count of all non-deleted binaries.
	Count(ctx context.Context) (int, error)
	// GetDefault returns the default binary for a given type, or nil.
	GetDefault(ctx context.Context, typ string) (*model.BinaryItem, error)
	// SoftDelete soft-deletes a binary by setting deleted_at and is_present=0.
	SoftDelete(ctx context.Context, id string) error
	// UpdateManyIsPresent bulk updates is_present flag for multiple binaries.
	UpdateManyIsPresent(ctx context.Context, ids []string, present bool) error
}
