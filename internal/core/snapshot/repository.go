// Package snapshot provides snapshot lifecycle management.
// Layer: Core domain — never imports other core/* packages.
package snapshot

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository is the data access interface for snapshots.
type Repository interface {
	// Basic CRUD
	Get(ctx context.Context, id string) (*model.SnapshotItem, error)
	GetByName(ctx context.Context, name string) (*model.SnapshotItem, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.SnapshotItem, error)
	ListAll(ctx context.Context) ([]*model.SnapshotItem, error)

	// Mutations
	Upsert(ctx context.Context, item *model.SnapshotItem) error
	Delete(ctx context.Context, id string) error

	// Reference counting for delete protection
	CountByKernelID(ctx context.Context, kernelID string) (int, error)
	CountByNetworkID(ctx context.Context, networkID string) (int, error)
	CountByBinaryID(ctx context.Context, binaryID string) (int, error)

	// Reference queries (for enricher reverse-relation)
	FindByKernelID(ctx context.Context, kernelID string) ([]*model.SnapshotItem, error)
	FindByKernelIDs(ctx context.Context, kernelIDs []string) ([]*model.SnapshotItem, error)
	FindByNetworkID(ctx context.Context, networkID string) ([]*model.SnapshotItem, error)
	FindByNetworkIDs(ctx context.Context, networkIDs []string) ([]*model.SnapshotItem, error)
	FindByBinaryID(ctx context.Context, binaryID string) ([]*model.SnapshotItem, error)
	FindByBinaryIDs(ctx context.Context, binaryIDs []string) ([]*model.SnapshotItem, error)
}
