package vm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &sqliteRepo{db: db}
}

const vmBaseQuery = "SELECT * FROM vm_instances"

// --- Basic CRUD ---

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.VMItem, error) {
	var vm model.VMItem
	err := sqlx.GetContext(ctx, r.db, &vm, vmBaseQuery+" WHERE id = ?", id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &vm, err
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.VMItem, error) {
	var vm model.VMItem
	err := sqlx.GetContext(ctx, r.db, &vm, vmBaseQuery+" WHERE name = ?", name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &vm, err
}

func (r *sqliteRepo) FindByIP(ctx context.Context, ipv4 string) (*model.VMItem, error) {
	var vm model.VMItem
	err := sqlx.GetContext(ctx, r.db, &vm, vmBaseQuery+" WHERE ipv4 = ?", ipv4)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &vm, err
}

func (r *sqliteRepo) FindByMAC(ctx context.Context, mac string) (*model.VMItem, error) {
	var vm model.VMItem
	err := sqlx.GetContext(ctx, r.db, &vm, vmBaseQuery+" WHERE mac = ?", mac)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &vm, err
}

func (r *sqliteRepo) NamesExist(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In("SELECT name FROM vm_instances WHERE name IN (?) ORDER BY name", names)
	if err != nil {
		return nil, fmt.Errorf("names exist: %w", err)
	}
	query = r.db.Rebind(query)
	var existingNames []string
	if err := r.db.SelectContext(ctx, &existingNames, query, args...); err != nil {
		return nil, fmt.Errorf("names exist: %w", err)
	}
	return existingNames, nil
}

// --- Lookups ---

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.VMItem, error) {
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, vmBaseQuery+" WHERE id LIKE ?", prefix+"%"); err != nil {
		return nil, fmt.Errorf("find vm by prefix: %w", err)
	}
	return rows, nil
}

// --- Counting ---

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var c int
	if err := sqlx.GetContext(ctx, r.db, &c, "SELECT COUNT(*) FROM vm_instances"); err != nil {
		return 0, err
	}
	return c, nil
}

func (r *sqliteRepo) CountByStatus(ctx context.Context, statuses ...string) (int, error) {
	if len(statuses) == 0 {
		return r.Count(ctx)
	}
	query, args, err := sqlx.In("SELECT COUNT(*) FROM vm_instances WHERE status IN (?)", statuses)
	if err != nil {
		return 0, fmt.Errorf("count vms by status: %w", err)
	}
	query = r.db.Rebind(query)
	var c int
	if err := sqlx.GetContext(ctx, r.db, &c, query, args...); err != nil {
		return 0, err
	}
	return c, nil
}

func (r *sqliteRepo) FindByNetworkID(ctx context.Context, networkID string) ([]*model.VMItem, error) {
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, vmBaseQuery+" WHERE network_id = ?", networkID); err != nil {
		return nil, fmt.Errorf("find vms by network id: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) GetByNetworkIDs(ctx context.Context, networkIDs []string) ([]*model.VMItem, error) {
	if len(networkIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(vmBaseQuery+" WHERE network_id IN (?)", networkIDs)
	if err != nil {
		return nil, fmt.Errorf("get vms by network ids: %w", err)
	}
	query = r.db.Rebind(query)
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("get vms by network ids: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByKernelID(ctx context.Context, kernelID string) ([]*model.VMItem, error) {
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, vmBaseQuery+" WHERE kernel_id = ?", kernelID); err != nil {
		return nil, fmt.Errorf("find vms by kernel id: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) GetByKernelIDs(ctx context.Context, kernelIDs []string) ([]*model.VMItem, error) {
	if len(kernelIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(vmBaseQuery+" WHERE kernel_id IN (?)", kernelIDs)
	if err != nil {
		return nil, fmt.Errorf("get vms by kernel ids: %w", err)
	}
	query = r.db.Rebind(query)
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("get vms by kernel ids: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByBinaryID(ctx context.Context, binaryID string) ([]*model.VMItem, error) {
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, vmBaseQuery+" WHERE binary_id = ?", binaryID); err != nil {
		return nil, fmt.Errorf("find vms by binary id: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) GetByBinaryIDs(ctx context.Context, binaryIDs []string) ([]*model.VMItem, error) {
	if len(binaryIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(vmBaseQuery+" WHERE binary_id IN (?)", binaryIDs)
	if err != nil {
		return nil, fmt.Errorf("get vms by binary ids: %w", err)
	}
	query = r.db.Rebind(query)
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("get vms by binary ids: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) GetByImageIDs(ctx context.Context, imageIDs []string) ([]*model.VMItem, error) {
	if len(imageIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(vmBaseQuery+" WHERE image_id IN (?)", imageIDs)
	if err != nil {
		return nil, fmt.Errorf("get vms by image ids: %w", err)
	}
	query = r.db.Rebind(query)
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("get vms by image ids: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByVolumeID(ctx context.Context, volumeID string) ([]*model.VMItem, error) {
	var rows []*model.VMItem
	like := `%"` + volumeID + `"%`
	if err := sqlx.SelectContext(ctx, r.db, &rows, vmBaseQuery+" WHERE volume_ids LIKE ?", like); err != nil {
		return nil, fmt.Errorf("find vms by volume id: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByVolumeIDsBatch(ctx context.Context, volumeIDs []string) ([]*model.VMItem, error) {
	if len(volumeIDs) == 0 {
		return nil, nil
	}
	patterns := make([]string, len(volumeIDs))
	args := make([]any, len(volumeIDs))
	for i, vid := range volumeIDs {
		patterns[i] = "volume_ids LIKE ?"
		args[i] = `%"` + vid + `"%`
	}
	query := "SELECT DISTINCT vm_instances.* FROM vm_instances WHERE " + strings.Join(patterns, " OR ")
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("find vms by volume ids batch: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindBySSHKeyID(ctx context.Context, keyID string) ([]*model.VMItem, error) {
	var rows []*model.VMItem
	like := `%"` + keyID + `"%`
	if err := sqlx.SelectContext(ctx, r.db, &rows, vmBaseQuery+" WHERE ssh_keys LIKE ?", like); err != nil {
		return nil, fmt.Errorf("find vms by ssh key id: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.VMItem, error) {
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, vmBaseQuery+" ORDER BY created_at"); err != nil {
		return nil, fmt.Errorf("list all vms: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) ListByStatus(ctx context.Context, statuses ...string) ([]*model.VMItem, error) {
	if len(statuses) == 0 {
		return r.ListAll(ctx)
	}
	query, args, err := sqlx.In(vmBaseQuery+" WHERE status IN (?) ORDER BY created_at", statuses)
	if err != nil {
		return nil, fmt.Errorf("list vms by status: %w", err)
	}
	query = r.db.Rebind(query)
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list vms by status: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) ListExcludingStatuses(ctx context.Context, excluded ...string) ([]*model.VMItem, error) {
	if len(excluded) == 0 {
		return r.ListAll(ctx)
	}
	query, args, err := sqlx.In(vmBaseQuery+" WHERE status NOT IN (?) ORDER BY created_at", excluded)
	if err != nil {
		return nil, fmt.Errorf("list vms excluding statuses: %w", err)
	}
	query = r.db.Rebind(query)
	var rows []*model.VMItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list vms excluding statuses: %w", err)
	}
	return rows, nil
}

// --- Mutations ---

func (r *sqliteRepo) Upsert(ctx context.Context, vm *model.VMItem) error {

	var err error
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO vm_instances (
			id, name, status, pid, process_start_time, ipv4, mac, network_id, tap_device,
			image_id, kernel_id, binary_id, api_socket_path,
			relay_socket_path, config_path, cloud_init_mode,
			nocloud_net_port, nocloud_net_pid, relay_pid,
			exit_code, vcpu_count, mem_size_mib, disk_size_mib,
			rootfs_path, rootfs_suffix, pci_enabled, nested_virt,
			remote_exec,
			enable_logging, enable_metrics, enable_console,
			ssh_keys, ssh_user,
			created_at, updated_at,
			log_path, serial_output_path, lsm_flags, boot_args, volume_ids, cpu_config
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			status = excluded.status,
			pid = excluded.pid,
			process_start_time = excluded.process_start_time,
			ipv4 = excluded.ipv4,
			mac = excluded.mac,
			network_id = excluded.network_id,
			tap_device = excluded.tap_device,
			image_id = excluded.image_id,
			kernel_id = excluded.kernel_id,
			binary_id = excluded.binary_id,
			api_socket_path = excluded.api_socket_path,
			relay_socket_path = excluded.relay_socket_path,
			config_path = excluded.config_path,
			cloud_init_mode = excluded.cloud_init_mode,
			nocloud_net_port = excluded.nocloud_net_port,
			nocloud_net_pid = excluded.nocloud_net_pid,
			relay_pid = excluded.relay_pid,
			exit_code = excluded.exit_code,
			vcpu_count = excluded.vcpu_count,
			mem_size_mib = excluded.mem_size_mib,
			disk_size_mib = excluded.disk_size_mib,
			rootfs_path = excluded.rootfs_path,
			rootfs_suffix = excluded.rootfs_suffix,
			pci_enabled = excluded.pci_enabled,
			nested_virt = excluded.nested_virt,
			remote_exec = excluded.remote_exec,
			enable_logging = excluded.enable_logging,
			enable_metrics = excluded.enable_metrics,
			enable_console = excluded.enable_console,
			ssh_keys = excluded.ssh_keys,
			ssh_user = excluded.ssh_user,
			log_path = excluded.log_path,
			serial_output_path = excluded.serial_output_path,
			lsm_flags = excluded.lsm_flags,
			boot_args = excluded.boot_args,
			volume_ids = excluded.volume_ids,
			cpu_config = excluded.cpu_config,
			updated_at = CURRENT_TIMESTAMP
	`,
		vm.ID,
		vm.Name,
		vm.Status,
		vm.PID,
		vm.ProcessStartTime,
		vm.IPv4,
		vm.MAC,
		vm.NetworkID,
		vm.TapDevice,
		vm.ImageID,
		vm.KernelID,
		vm.BinaryID,
		vm.APISocketPath,
		vm.RelaySocketPath,
		vm.ConfigPath,
		vm.CloudInitMode,
		vm.NocloudNetPort,
		vm.NocloudNetPID,
		vm.RelayPID,
		vm.ExitCode,
		vm.VCPUCount,
		vm.MemSizeMiB,
		vm.DiskSizeMiB,
		vm.RootfsPath,
		vm.RootfsSuffix,
		vm.PCIEnabled,
		vm.NestedVirt,
		vm.RemoteExec,
		vm.EnableLogging,
		vm.EnableMetrics,
		vm.EnableConsole,
		vm.SSHKeys,
		vm.SSHUser,
		vm.CreatedAt,
		vm.UpdatedAt,
		vm.LogPath,
		vm.SerialOutputPath,
		vm.LSMFlags,
		vm.BootArgs,
		vm.VolumeIDs,
		vm.CPUConfig,
	)
	return err
}

func (r *sqliteRepo) UpdateStatus(ctx context.Context, id string, status model.VMStatus) error {
	_, err := r.db.ExecContext(
		ctx,
		"UPDATE vm_instances SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		status,
		id,
	)
	return err
}

func (r *sqliteRepo) UpdatePID(ctx context.Context, id string, pid *int) error {
	_, err := r.db.ExecContext(
		ctx,
		"UPDATE vm_instances SET pid = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		pid,
		id,
	)
	return err
}

func (r *sqliteRepo) UpdateProcessInfo(ctx context.Context, id string, pid *int, processStartTime *int64) error {
	_, err := r.db.ExecContext(
		ctx,
		"UPDATE vm_instances SET pid = ?, process_start_time = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		pid,
		processStartTime,
		id,
	)
	return err
}

func (r *sqliteRepo) UpdateExitCode(ctx context.Context, id string, exitCode int) error {
	_, err := r.db.ExecContext(
		ctx,
		"UPDATE vm_instances SET exit_code = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		exitCode,
		id,
	)
	return err
}

// --- Deletion ---

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM vm_instances WHERE id = ?", id)
	return err
}

func (r *sqliteRepo) DeleteMany(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	query, args, err := sqlx.In("DELETE FROM vm_instances WHERE id IN (?)", ids)
	if err != nil {
		return 0, err
	}
	query = r.db.Rebind(query)
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}
