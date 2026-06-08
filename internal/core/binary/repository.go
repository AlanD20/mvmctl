package binary

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository matches Python's Repository class methods exactly.
type Repository interface {
	// Get returns a binary by its full 64-char ID, or nil if not found.
	Get(ctx context.Context, id string) (*model.BinaryItem, error)
	// FindByPrefix returns all binaries whose ID starts with prefix.
	FindByPrefix(ctx context.Context, prefix string) ([]*model.BinaryItem, error)
	// ListAll returns all non-deleted binaries ordered by created_at.
	ListAll(ctx context.Context) ([]*model.BinaryItem, error)
	// ListByName returns all binaries with a given name.
	ListByName(ctx context.Context, name string) ([]*model.BinaryItem, error)
	// GetByNameAndVersion returns a binary by name and version, or nil.
	GetByNameAndVersion(ctx context.Context, name, version string) (*model.BinaryItem, error)
	// Upsert inserts or replaces a binary record.
	Upsert(ctx context.Context, binary *model.BinaryItem) error
	// Delete hard-deletes a binary by ID.
	Delete(ctx context.Context, id string) error
	// DeleteByName deletes ALL binary rows matching the given name.
	DeleteByName(ctx context.Context, name string) error
	// DeleteByNameAndVersion deletes the binary row matching name AND version.
	DeleteByNameAndVersion(ctx context.Context, name, version string) error
	// SetDefault sets a binary as default, clearing all others with the same name.
	SetDefault(ctx context.Context, name, version, path string) error
	// Count returns total count of all non-deleted binaries.
	Count(ctx context.Context) (int, error)
	// GetDefault returns the default binary for a given name, or nil.
	GetDefault(ctx context.Context, name string) (*model.BinaryItem, error)
	// SoftDelete soft-deletes a binary by setting deleted_at and is_present=0.
	SoftDelete(ctx context.Context, id string) error
	// UpdateManyIsPresent bulk updates is_present flag for multiple binaries.
	UpdateManyIsPresent(ctx context.Context, ids []string, present bool) error
}
