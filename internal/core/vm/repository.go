package vm

import (
	"context"

	"mvmctl/internal/lib/model"
)

// Repository is the data access interface for VM instances.
type Repository interface {
	// Basic CRUD
	Get(ctx context.Context, id string) (*model.VM, error)
	GetByName(ctx context.Context, name string) (*model.VM, error)
	// Returns the subset of names that already exist.
	NamesExist(ctx context.Context, names []string) ([]string, error)

	// Lookups by various fields
	FindByIP(ctx context.Context, ipv4 string) (*model.VM, error)
	FindByMAC(ctx context.Context, mac string) (*model.VM, error)
	FindByPrefix(ctx context.Context, prefix string) ([]*model.VM, error)

	// Counting
	Count(ctx context.Context) (int, error)
	CountByStatus(ctx context.Context, statuses ...string) (int, error)

	// Foreign key lookups
	FindByNetworkID(ctx context.Context, networkID string) ([]*model.VM, error)
	GetByNetworkIDs(ctx context.Context, networkIDs []string) ([]*model.VM, error)
	FindByKernelID(ctx context.Context, kernelID string) ([]*model.VM, error)
	GetByKernelIDs(ctx context.Context, kernelIDs []string) ([]*model.VM, error)
	FindByBinaryID(ctx context.Context, binaryID string) ([]*model.VM, error)
	GetByBinaryIDs(ctx context.Context, binaryIDs []string) ([]*model.VM, error)
	GetByImageIDs(ctx context.Context, imageIDs []string) ([]*model.VM, error)

	// Volume lookups (volume_ids is a JSON array in DB)
	FindByVolumeID(ctx context.Context, volumeID string) ([]*model.VM, error)
	FindByVolumeIDsBatch(ctx context.Context, volumeIDs []string) ([]*model.VM, error)

	// SSH key lookup (ssh_keys is a JSON array in DB)
	FindBySSHKeyID(ctx context.Context, keyID string) ([]*model.VM, error)

	// Listing
	ListAll(ctx context.Context) ([]*model.VM, error)
	ListByStatus(ctx context.Context, statuses ...string) ([]*model.VM, error)
	ListExcludingStatuses(ctx context.Context, excluded ...string) ([]*model.VM, error)

	// Mutations
	Upsert(ctx context.Context, vm *model.VM) error
	UpdateStatus(ctx context.Context, id string, status model.VMStatus) error
	// nil pid clears the PID field.
	UpdatePID(ctx context.Context, id string, pid *int) error
	UpdateProcessInfo(ctx context.Context, id string, pid *int, processStartTime *int64) error
	UpdateExitCode(ctx context.Context, id string, exitCode int) error

	// Deletion
	Delete(ctx context.Context, id string) error
	DeleteMany(ctx context.Context, ids []string) (int, error)
}
