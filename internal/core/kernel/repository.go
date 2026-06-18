package kernel

import (
	"context"

	"mvmctl/internal/lib/model"
)

type Repository interface {
	// Get returns a kernel by its full 64-char ID, or nil if not found.
	Get(ctx context.Context, id string) (*model.KernelItem, error)
	// FindByPrefix returns all kernels whose ID starts with prefix.
	FindByPrefix(ctx context.Context, prefix string) ([]*model.KernelItem, error)
	// Count returns total count of all non-deleted kernels.
	Count(ctx context.Context) (int, error)
	// ListAll returns all non-deleted kernels ordered by created_at.
	ListAll(ctx context.Context) ([]*model.KernelItem, error)
	// Upsert inserts or replaces a kernel record.
	Upsert(ctx context.Context, kernel *model.KernelItem) error
	// SoftDelete sets deleted_at and is_present=0.
	SoftDelete(ctx context.Context, id string) error
	// Delete removes a kernel record permanently.
	Delete(ctx context.Context, id string) error
	// SetDefault sets one kernel as default, clearing all others atomically.
	SetDefault(ctx context.Context, id string) error
	// GetDefault returns the default kernel, or nil if not set.
	GetDefault(ctx context.Context) (*model.KernelItem, error)
	// GetByName returns a kernel by its name, or nil.
	GetByName(ctx context.Context, name string) (*model.KernelItem, error)
	// GetByType returns a kernel by its type, or nil.
	GetByType(ctx context.Context, kernelType string) (*model.KernelItem, error)
	// GetByVersionAndType returns a kernel by version and type, or nil.
	GetByVersionAndType(ctx context.Context, version, kernelType string) (*model.KernelItem, error)
	// UpdateManyIsPresent bulk-updates the is_present flag.
	UpdateManyIsPresent(ctx context.Context, ids []string, isPresent bool) error
}
