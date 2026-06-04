package vm

import (
	"context"

	"mvmctl/internal/infra/model"
)

// Repository is the data access interface for VM instances.
// Matches Python's Repository class methods exactly.
type Repository interface {
	// Basic CRUD
	// Python: get(vm_id) -> VMInstanceItem | None
	Get(ctx context.Context, id string) (*model.VM, error)
	// Python: get_by_name(name) -> VMInstanceItem | None
	GetByName(ctx context.Context, name string) (*model.VM, error)
	// Python: get_by_names(names) -> set[str]
	// Returns set of names (as map keys) that already exist.
	GetByNames(ctx context.Context, names []string) (map[string]bool, error)

	// Lookups by various fields
	// Python: find_by_ip(ipv4) -> VMInstanceItem | None
	FindByIP(ctx context.Context, ipv4 string) (*model.VM, error)
	// Python: find_by_mac(mac) -> VMInstanceItem | None
	FindByMAC(ctx context.Context, mac string) (*model.VM, error)
	// Python: find_by_prefix(prefix) -> list[VMInstanceItem]
	FindByPrefix(ctx context.Context, prefix string) ([]*model.VM, error)

	// Counting
	// Python: count() -> int
	Count(ctx context.Context) (int, error)
	// Python: count_by_status(status) -> int (accepts VMStatus | list[VMStatus])
	CountByStatus(ctx context.Context, statuses ...string) (int, error)

	// Foreign key lookups
	// Python: find_by_network_id(network_id) -> list[VMInstanceItem]
	FindByNetworkID(ctx context.Context, networkID string) ([]*model.VM, error)
	// Python: get_by_network_ids(network_ids) -> list[VMInstanceItem]
	GetByNetworkIDs(ctx context.Context, networkIDs []string) ([]*model.VM, error)
	// Python: find_by_kernel_id(kernel_id) -> list[VMInstanceItem]
	FindByKernelID(ctx context.Context, kernelID string) ([]*model.VM, error)
	// Python: get_by_kernel_ids(kernel_ids) -> list[VMInstanceItem]
	GetByKernelIDs(ctx context.Context, kernelIDs []string) ([]*model.VM, error)
	// Python: find_by_binary_id(binary_id) -> list[VMInstanceItem]
	FindByBinaryID(ctx context.Context, binaryID string) ([]*model.VM, error)
	// Python: get_by_binary_ids(binary_ids) -> list[VMInstanceItem]
	GetByBinaryIDs(ctx context.Context, binaryIDs []string) ([]*model.VM, error)
	// Python: get_by_image_ids(image_ids) -> list[VMInstanceItem]
	GetByImageIDs(ctx context.Context, imageIDs []string) ([]*model.VM, error)

	// Volume lookups (volume_ids is a JSON array in DB)
	// Python: find_by_volume_id(volume_id) -> list[VMInstanceItem]
	FindByVolumeID(ctx context.Context, volumeID string) ([]*model.VM, error)
	// Python: find_by_volume_ids_batch(volume_ids) -> list[VMInstanceItem]
	FindByVolumeIDsBatch(ctx context.Context, volumeIDs []string) ([]*model.VM, error)

	// SSH key lookup (ssh_keys is a JSON array in DB)
	// Python: find_by_ssh_key_id(key_id) -> list[VMInstanceItem]
	FindBySSHKeyID(ctx context.Context, keyID string) ([]*model.VM, error)

	// Listing
	// Python: list_all() -> list[VMInstanceItem]
	ListAll(ctx context.Context) ([]*model.VM, error)
	// Python: list_by_status(status) -> list[VMInstanceItem] (accepts VMStatus | list[VMStatus])
	ListByStatus(ctx context.Context, statuses ...string) ([]*model.VM, error)
	// Python: list_excluding_statuses(statuses) -> list[VMInstanceItem]
	ListExcludingStatuses(ctx context.Context, excluded ...string) ([]*model.VM, error)

	// Mutations
	// Python: upsert(vm: VMInstanceItem) -> None
	Upsert(ctx context.Context, vm *model.VM) error
	// Python: update_status(vm_id, status) -> None
	UpdateStatus(ctx context.Context, id string, status model.VMStatus) error
	// Python: update_pid(vm_id, pid: int | None) -> None
	// nil pid clears the PID field.
	UpdatePID(ctx context.Context, id string, pid *int) error
	// Python: update_process_info(vm_id, pid: int | None, process_start_time: int | None) -> None
	UpdateProcessInfo(ctx context.Context, id string, pid *int, processStartTime *int64) error
	// Python: update_exit_code(vm_id, exit_code) -> None
	UpdateExitCode(ctx context.Context, id string, exitCode int) error

	// Deletion
	// Python: delete(vm_id) -> None
	Delete(ctx context.Context, id string) error
	// Python: delete_many(vm_ids) -> int
	DeleteMany(ctx context.Context, ids []string) (int, error)
}
