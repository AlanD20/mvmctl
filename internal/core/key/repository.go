package key

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository defines all database operations for SSH keys.
// Matches Python's Repository exactly.
type Repository interface {
	// GetByName returns an SSH key by name, or nil if not found.
	GetByName(ctx context.Context, name string) (*model.SSHKeyItem, error)

	// FindByPrefix returns all SSH keys whose ID starts with prefix.
	FindByPrefix(ctx context.Context, prefix string) ([]*model.SSHKeyItem, error)

	// Count returns the total count of all SSH keys.
	Count(ctx context.Context) (int, error)

	// List returns all SSH keys ordered by created_at.
	List(ctx context.Context) ([]*model.SSHKeyItem, error)

	// Upsert inserts or replaces an SSH key record.
	Upsert(ctx context.Context, key *model.SSHKeyItem) error

	// UpdateManyIsPresent bulk updates the is_present flag for multiple keys.
	UpdateManyIsPresent(ctx context.Context, ids []string, present bool) error

	// Delete removes an SSH key by ID. No-op if not found.
	Delete(ctx context.Context, id string) error

	// SetDefault sets an SSH key as default (does NOT clear other defaults).
	SetDefault(ctx context.Context, id string) error

	// GetDefaults returns all SSH keys marked as default.
	GetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error)

	// ClearDefaults clears all default SSH keys.
	ClearDefaults(ctx context.Context) error
}
