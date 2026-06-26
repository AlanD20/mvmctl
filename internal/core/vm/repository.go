package vm

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository is the data access interface for VM instances.
type Repository interface {
	// Basic CRUD
	Get(ctx context.Context, id string) (*model.VMItem, error)
	GetByName(ctx context.Context, name string) (*model.VMItem, error)
	// Returns the subset of names that already exist.
	NamesExist(ctx context.Context, names []string) ([]string, error)

	// Lookups by various fields
	FindByIP(ctx context.Context, ipv4 string) (*model.VMItem, error)
	FindByMAC(ctx context.Context, mac string) (*model.VMItem, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.VMItem, error)

	// Counting
	Count(ctx context.Context) (int, error)
	CountByStatus(ctx context.Context, statuses ...string) (int, error)

	// Foreign key lookups
	FindByNetworkID(ctx context.Context, networkID string) ([]*model.VMItem, error)
	GetByNetworkIDs(ctx context.Context, networkIDs []string) ([]*model.VMItem, error)
	FindByKernelID(ctx context.Context, kernelID string) ([]*model.VMItem, error)
	GetByKernelIDs(ctx context.Context, kernelIDs []string) ([]*model.VMItem, error)
	FindByBinaryID(ctx context.Context, binaryID string) ([]*model.VMItem, error)
	GetByBinaryIDs(ctx context.Context, binaryIDs []string) ([]*model.VMItem, error)
	GetByImageIDs(ctx context.Context, imageIDs []string) ([]*model.VMItem, error)

	// Volume lookups (volume_ids is a JSON array in DB)
	FindByVolumeID(ctx context.Context, volumeID string) ([]*model.VMItem, error)
	FindByVolumeIDsBatch(ctx context.Context, volumeIDs []string) ([]*model.VMItem, error)

	// SSH key lookup (ssh_keys is a JSON array in DB)
	FindBySSHKeyID(ctx context.Context, keyID string) ([]*model.VMItem, error)

	// Listing
	ListAll(ctx context.Context) ([]*model.VMItem, error)
	ListByStatus(ctx context.Context, statuses ...string) ([]*model.VMItem, error)
	ListExcludingStatuses(ctx context.Context, excluded ...string) ([]*model.VMItem, error)

	// Mutations
	Upsert(ctx context.Context, vm *model.VMItem) error
	UpdateStatus(ctx context.Context, id string, status model.VMStatus) error
	// nil pid clears the PID field.
	UpdatePID(ctx context.Context, id string, pid *int) error
	UpdateProcessInfo(ctx context.Context, id string, pid *int, processStartTime *int64) error
	UpdateExitCode(ctx context.Context, id string, exitCode int) error

	// Deletion
	Delete(ctx context.Context, id string) error
	DeleteMany(ctx context.Context, ids []string) (int, error)
}
