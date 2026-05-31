package vm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &sqliteRepo{db: db}
}

// Matches Python's "SELECT * FROM vm_instances" exactly.
const vmBaseQuery = "SELECT * FROM vm_instances"

// ── Basic CRUD ──

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.VM, error) {
	var v vmScanRow
	err := sqlx.GetContext(ctx, r.db, &v, vmBaseQuery+" WHERE id = ?", id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v.toVM()
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.VM, error) {
	var v vmScanRow
	err := sqlx.GetContext(ctx, r.db, &v, vmBaseQuery+" WHERE name = ?", name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v.toVM()
}

func (r *sqliteRepo) GetByNames(ctx context.Context, names []string) (map[string]bool, error) {
	// Python: no @_graceful_read on this method.
	if len(names) == 0 {
		return map[string]bool{}, nil
	}
	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for i, n := range names {
		placeholders[i] = "?"
		args[i] = n
	}
	query := "SELECT name FROM vm_instances WHERE name IN (" + strings.Join(placeholders, ",") + ")"
	var vmNames []struct {
		Name string `db:"name"`
	}
	if err := r.db.SelectContext(ctx, &vmNames, query, args...); err != nil {
		return nil, fmt.Errorf("get vms by names: %w", err)
	}
	result := make(map[string]bool)
	for _, r := range vmNames {
		result[r.Name] = true
	}
	return result, nil
}

// ── Lookups ──

func (r *sqliteRepo) FindByIP(ctx context.Context, ipv4 string) (*model.VM, error) {
	var v vmScanRow
	err := sqlx.GetContext(ctx, r.db, &v, vmBaseQuery+" WHERE ipv4 = ?", ipv4)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v.toVM()
}

func (r *sqliteRepo) FindByMAC(ctx context.Context, mac string) (*model.VM, error) {
	var v vmScanRow
	err := sqlx.GetContext(ctx, r.db, &v, vmBaseQuery+" WHERE mac = ?", mac)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v.toVM()
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.VM, error) {
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE id LIKE ?", prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("find vm by prefix: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

// ── Counting ──

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var c int
	err := sqlx.GetContext(ctx, r.db, &c, "SELECT COUNT(*) FROM vm_instances")
	return c, err
}

func (r *sqliteRepo) CountByStatus(ctx context.Context, statuses ...string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(statuses) == 0 {
		return r.Count(ctx)
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, s := range statuses {
		placeholders[i] = "?"
		args[i] = s
	}
	query := "SELECT COUNT(*) FROM vm_instances WHERE status IN (" + strings.Join(placeholders, ",") + ")"
	var c int
	err := sqlx.GetContext(ctx, r.db, &c, query, args...)
	return c, err
}

// ── Foreign key lookups ──

func (r *sqliteRepo) FindByNetworkID(ctx context.Context, networkID string) ([]*model.VM, error) {
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE network_id = ?", networkID)
	if err != nil {
		return nil, fmt.Errorf("find vms by network id: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) GetByNetworkIDs(ctx context.Context, networkIDs []string) ([]*model.VM, error) {
	if len(networkIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(networkIDs))
	args := make([]any, len(networkIDs))
	for i, nid := range networkIDs {
		placeholders[i] = "?"
		args[i] = nid
	}
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE network_id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, fmt.Errorf("get vms by network ids: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) FindByKernelID(ctx context.Context, kernelID string) ([]*model.VM, error) {
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE kernel_id = ?", kernelID)
	if err != nil {
		return nil, fmt.Errorf("find vms by kernel id: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) GetByKernelIDs(ctx context.Context, kernelIDs []string) ([]*model.VM, error) {
	if len(kernelIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(kernelIDs))
	args := make([]any, len(kernelIDs))
	for i, kid := range kernelIDs {
		placeholders[i] = "?"
		args[i] = kid
	}
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE kernel_id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, fmt.Errorf("get vms by kernel ids: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) FindByBinaryID(ctx context.Context, binaryID string) ([]*model.VM, error) {
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE binary_id = ?", binaryID)
	if err != nil {
		return nil, fmt.Errorf("find vms by binary id: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) GetByBinaryIDs(ctx context.Context, binaryIDs []string) ([]*model.VM, error) {
	if len(binaryIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(binaryIDs))
	args := make([]any, len(binaryIDs))
	for i, bid := range binaryIDs {
		placeholders[i] = "?"
		args[i] = bid
	}
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE binary_id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, fmt.Errorf("get vms by binary ids: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) GetByImageIDs(ctx context.Context, imageIDs []string) ([]*model.VM, error) {
	if len(imageIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(imageIDs))
	args := make([]any, len(imageIDs))
	for i, iid := range imageIDs {
		placeholders[i] = "?"
		args[i] = iid
	}
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE image_id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, fmt.Errorf("get vms by image ids: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

// ── Volume lookups (JSON array match) ──

func (r *sqliteRepo) FindByVolumeID(ctx context.Context, volumeID string) ([]*model.VM, error) {
	// Python: "SELECT * FROM vm_instances WHERE volume_ids LIKE ?" with '%"{volume_id}"%'
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE volume_ids LIKE ?", `%`+`"`+volumeID+`"`+`%`)
	if err != nil {
		return nil, fmt.Errorf("find vms by volume id: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) FindByVolumeIDsBatch(ctx context.Context, volumeIDs []string) ([]*model.VM, error) {
	if len(volumeIDs) == 0 {
		return nil, nil
	}
	// Python: patterns = [f'%"{vid}"%' for vid in volume_ids]
	// "SELECT DISTINCT vm_instances.* FROM vm_instances WHERE " + " OR ".join('volume_ids LIKE ?')
	patterns := make([]string, len(volumeIDs))
	args := make([]any, len(volumeIDs))
	for i, vid := range volumeIDs {
		patterns[i] = "volume_ids LIKE ?"
		args[i] = `%` + `"` + vid + `"` + `%`
	}
	// Python uses DISTINCT since multiple volume_ids can match the same VM
	query := "SELECT DISTINCT vm_instances.* FROM vm_instances WHERE " + strings.Join(patterns, " OR ")
	rows, err := r.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find vms by volume ids batch: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

// ── SSH key lookup ──

func (r *sqliteRepo) FindBySSHKeyID(ctx context.Context, keyID string) ([]*model.VM, error) {
	// Python: no @_graceful_read on this method.
	// Python: "SELECT * FROM vm_instances WHERE ssh_keys LIKE ?" with '%"{key_id}"%'
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" WHERE ssh_keys LIKE ?", `%`+`"`+keyID+`"`+`%`)
	if err != nil {
		return nil, fmt.Errorf("find vms by ssh key id: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

// ── Listing ──

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.VM, error) {
	// Python: "SELECT * FROM vm_instances ORDER BY created_at"
	rows, err := r.db.QueryxContext(ctx, vmBaseQuery+" ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("list all vms: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) ListByStatus(ctx context.Context, statuses ...string) ([]*model.VM, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(statuses) == 0 {
		return r.ListAll(ctx)
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, s := range statuses {
		placeholders[i] = "?"
		args[i] = s
	}
	// Python: "SELECT * FROM vm_instances WHERE status IN (...) ORDER BY created_at"
	query := vmBaseQuery + " WHERE status IN (" + strings.Join(placeholders, ",") + ") ORDER BY created_at"
	rows, err := r.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list vms by status: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

func (r *sqliteRepo) ListExcludingStatuses(ctx context.Context, excluded ...string) ([]*model.VM, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(excluded) == 0 {
		return r.ListAll(ctx)
	}
	placeholders := make([]string, len(excluded))
	args := make([]any, len(excluded))
	for i, s := range excluded {
		placeholders[i] = "?"
		args[i] = s
	}
	// Python: "SELECT * FROM vm_instances WHERE status NOT IN (...) ORDER BY created_at"
	query := vmBaseQuery + " WHERE status NOT IN (" + strings.Join(placeholders, ",") + ") ORDER BY created_at"
	rows, err := r.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list vms excluding statuses: %w", err)
	}
	defer rows.Close()
	var items []*model.VM
	for rows.Next() {
		var v vmScanRow
		if err := rows.StructScan(&v); err != nil {
			return nil, fmt.Errorf("scan vm: %w", err)
		}
		vm, err := v.toVM()
		if err != nil {
			return nil, err
		}
		items = append(items, vm)
	}
	return items, rows.Err()
}

// ── Mutations ──

func (r *sqliteRepo) Upsert(ctx context.Context, vm *model.VM) error {
	// Python: no @_graceful_read on this method.
	sshKeysJSON, err := MarshalSSHKeys(vm.SSHKeys)
	if err != nil {
		return fmt.Errorf("marshal ssh_keys: %w", err)
	}
	volumeIDsJSON, err := MarshalVolumeIDs(vm.VolumeIDs)
	if err != nil {
		return fmt.Errorf("marshal volume_ids: %w", err)
	}
	cpuConfigJSON, err := MarshalCPUConfig(vm.CPUConfig)
	if err != nil {
		return fmt.Errorf("marshal cpu_config: %w", err)
	}

	// Python's exact UPSERT query with ALL columns and ON CONFLICT DO UPDATE
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO vm_instances (
			id, name, status, pid, process_start_time, ipv4, mac, network_id, tap_device,
			image_id, kernel_id, binary_id, api_socket_path,
			relay_socket_path, config_path, cloud_init_mode,
			nocloud_net_port, nocloud_net_pid, relay_pid,
			exit_code, vcpu_count, mem_size_mib, disk_size_mib,
			rootfs_path, rootfs_suffix, pci_enabled, nested_virt,
			enable_logging, enable_metrics, enable_console,
			ssh_keys, ssh_user,
			created_at, updated_at,
			log_path, serial_output_path, lsm_flags, boot_args, volume_ids, cpu_config
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		vm.EnableLogging,
		vm.EnableMetrics,
		vm.EnableConsole,
		sshKeysJSON,
		vm.SSHUser,
		vm.CreatedAt,
		vm.UpdatedAt,
		vm.LogPath,
		vm.SerialOutputPath,
		vm.LSMFlags,
		vm.BootArgs,
		volumeIDsJSON,
		cpuConfigJSON,
	)
	return err
}

func (r *sqliteRepo) UpdateStatus(ctx context.Context, id string, status model.Status) error {
	// Python: "UPDATE vm_instances SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?"
	_, err := r.db.ExecContext(ctx, "UPDATE vm_instances SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", status, id)
	return err
}

func (r *sqliteRepo) UpdatePID(ctx context.Context, id string, pid *int) error {
	// Python: "UPDATE vm_instances SET pid = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?"
	_, err := r.db.ExecContext(ctx, "UPDATE vm_instances SET pid = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", pid, id)
	return err
}

func (r *sqliteRepo) UpdateProcessInfo(ctx context.Context, id string, pid *int, processStartTime *int64) error {
	// Python: "UPDATE vm_instances SET pid = ?, process_start_time = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?"
	_, err := r.db.ExecContext(ctx, "UPDATE vm_instances SET pid = ?, process_start_time = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", pid, processStartTime, id)
	return err
}

func (r *sqliteRepo) UpdateExitCode(ctx context.Context, id string, exitCode int) error {
	// Python: "UPDATE vm_instances SET exit_code = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?"
	_, err := r.db.ExecContext(ctx, "UPDATE vm_instances SET exit_code = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", exitCode, id)
	return err
}

// ── Deletion ──

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM vm_instances WHERE id = ?", id)
	return err
}

func (r *sqliteRepo) DeleteMany(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	result, err := r.db.ExecContext(ctx, "DELETE FROM vm_instances WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// ── Scan helpers ──
// These scan SQLite rows into VM structs with JSON deserialization matching Python's __post_init__.

// scanVM scans a single SQLite row into a VM struct.
// The scan order MUST match the CREATE TABLE column order (not the INSERT column order)
// because SELECT * returns columns in CREATE TABLE order.
//
// CREATE TABLE column order (from 001_initial_schema.sql lines 122-167):
//
//	 1: id                   2: name                  3: status
//	 4: pid                  5: process_start_time    6: ipv4
//	 7: mac                  8: network_id            9: tap_device
//	10: image_id            11: kernel_id            12: binary_id
//	13: api_socket_path     14: relay_socket_path    15: config_path
//	16: cloud_init_mode     17: nocloud_net_port     18: nocloud_net_pid
//	19: relay_pid           20: exit_code            21: log_path
//	22: serial_output_path  23: vcpu_count           24: mem_size_mib
//	25: disk_size_mib       26: rootfs_path          27: rootfs_suffix
//	28: pci_enabled         29: nested_virt          30: cpu_config
//	31: lsm_flags           32: enable_logging       33: enable_metrics
//	34: enable_console      35: boot_args            36: ssh_keys
//	37: ssh_user            38: volume_ids           39: created_at
//	40: updated_at

// vmScanRow for scanning raw SQLite rows with sqlx.StructScan.
// Bool columns use sql.NullInt64 (SQLite stores bool as 0/1).
// JSON columns use sql.NullString and are deserialized by toVM().
type vmScanRow struct {
	ID               string         `db:"id"`
	Name             string         `db:"name"`
	Status           string         `db:"status"`
	PID              int            `db:"pid"`
	ProcessStartTime sql.NullInt64  `db:"process_start_time"`
	IPv4             string         `db:"ipv4"`
	MAC              string         `db:"mac"`
	NetworkID        string         `db:"network_id"`
	TapDevice        string         `db:"tap_device"`
	ImageID          string         `db:"image_id"`
	KernelID         string         `db:"kernel_id"`
	BinaryID         string         `db:"binary_id"`
	APISocketPath    string         `db:"api_socket_path"`
	RelaySocketPath  sql.NullString `db:"relay_socket_path"`
	ConfigPath       string         `db:"config_path"`
	CloudInitMode    string         `db:"cloud_init_mode"`
	NocloudNetPort   sql.NullInt64  `db:"nocloud_net_port"`
	NocloudNetPID    sql.NullInt64  `db:"nocloud_net_pid"`
	RelayPID         sql.NullInt64  `db:"relay_pid"`
	ExitCode         sql.NullInt64  `db:"exit_code"`
	LogPath          sql.NullString `db:"log_path"`
	SerialOutputPath sql.NullString `db:"serial_output_path"`
	VCPUCount        int            `db:"vcpu_count"`
	MemSizeMiB       int            `db:"mem_size_mib"`
	DiskSizeMiB      int            `db:"disk_size_mib"`
	RootfsPath       string         `db:"rootfs_path"`
	RootfsSuffix     string         `db:"rootfs_suffix"`
	PCIEnabled       sql.NullInt64  `db:"pci_enabled"`
	NestedVirt       sql.NullInt64  `db:"nested_virt"`
	CPUConfig        sql.NullString `db:"cpu_config"`
	LSMFlags         sql.NullString `db:"lsm_flags"`
	EnableLogging    sql.NullInt64  `db:"enable_logging"`
	EnableMetrics    sql.NullInt64  `db:"enable_metrics"`
	EnableConsole    sql.NullInt64  `db:"enable_console"`
	BootArgs         sql.NullString `db:"boot_args"`
	SSHKeys          sql.NullString `db:"ssh_keys"`
	SSHUser          sql.NullString `db:"ssh_user"`
	VolumeIDs        sql.NullString `db:"volume_ids"`
	CreatedAt        string         `db:"created_at"`
	UpdatedAt        string         `db:"updated_at"`
}

func (v *vmScanRow) toVM() (*model.VM, error) {
	vm := &model.VM{
		ID:            v.ID,
		Name:          v.Name,
		Status:        model.Status(v.Status),
		PID:           v.PID,
		IPv4:          v.IPv4,
		MAC:           v.MAC,
		NetworkID:     v.NetworkID,
		TapDevice:     v.TapDevice,
		ImageID:       v.ImageID,
		KernelID:      v.KernelID,
		BinaryID:      v.BinaryID,
		APISocketPath: v.APISocketPath,
		ConfigPath:    v.ConfigPath,
		CloudInitMode: v.CloudInitMode,
		VCPUCount:     v.VCPUCount,
		MemSizeMiB:    v.MemSizeMiB,
		DiskSizeMiB:   v.DiskSizeMiB,
		RootfsPath:    v.RootfsPath,
		RootfsSuffix:  v.RootfsSuffix,
		CreatedAt:     v.CreatedAt,
		UpdatedAt:     v.UpdatedAt,
	}

	// Process start time
	if v.ProcessStartTime.Valid {
		vm.ProcessStartTime = &v.ProcessStartTime.Int64
	}

	// Relay socket path
	if v.RelaySocketPath.Valid {
		vm.RelaySocketPath = &v.RelaySocketPath.String
	}

	// Nocloud net port
	if v.NocloudNetPort.Valid {
		n := int(v.NocloudNetPort.Int64)
		vm.NocloudNetPort = &n
	}

	// Nocloud net PID
	if v.NocloudNetPID.Valid {
		n := int(v.NocloudNetPID.Int64)
		vm.NocloudNetPID = &n
	}

	// Relay PID
	if v.RelayPID.Valid {
		n := int(v.RelayPID.Int64)
		vm.RelayPID = &n
	}

	// Exit code
	if v.ExitCode.Valid {
		n := int(v.ExitCode.Int64)
		vm.ExitCode = &n
	}

	// Log path
	if v.LogPath.Valid {
		vm.LogPath = &v.LogPath.String
	}

	// Serial output path
	if v.SerialOutputPath.Valid {
		vm.SerialOutputPath = &v.SerialOutputPath.String
	}

	// LSM flags
	if v.LSMFlags.Valid {
		vm.LSMFlags = &v.LSMFlags.String
	}

	// Boot args
	if v.BootArgs.Valid {
		vm.BootArgs = &v.BootArgs.String
	}

	// SSH user
	if v.SSHUser.Valid {
		vm.SSHUser = &v.SSHUser.String
	}

	// Bool fields — match Python's CommonUtils.coerce_bool_fields() using bool(value).
	// Python's bool(0) == False, bool(None) == False, bool(1) == True, bool(2) == True.
	// Using != 0 matches Python's bool() exactly for any integer value.
	vm.PCIEnabled = v.PCIEnabled.Valid && v.PCIEnabled.Int64 != 0
	vm.NestedVirt = v.NestedVirt.Valid && v.NestedVirt.Int64 != 0
	vm.EnableLogging = v.EnableLogging.Valid && v.EnableLogging.Int64 != 0
	vm.EnableMetrics = v.EnableMetrics.Valid && v.EnableMetrics.Int64 != 0
	vm.EnableConsole = v.EnableConsole.Valid && v.EnableConsole.Int64 != 0

	// JSON fields — deserialize like Python's __post_init__
	// Propagate JSON errors (matches Python's json.JSONDecodeError raising).
	if v.SSHKeys.Valid {
		keys, err := UnmarshalSSHKeys(v.SSHKeys.String)
		if err != nil {
			return nil, fmt.Errorf("unmarshal ssh_keys: %w", err)
		}
		vm.SSHKeys = keys
	}
	// When SSHKeys column is NULL, vm.SSHKeys stays nil — matches Python's
	// behavior where VMInstanceItem(**dict(row)) with None for ssh_keys from
	// a NULL column results in self.ssh_keys being None (not []).

	if v.VolumeIDs.Valid {
		ids, err := UnmarshalVolumeIDs(&v.VolumeIDs.String)
		if err != nil {
			return nil, fmt.Errorf("unmarshal volume_ids: %w", err)
		}
		vm.VolumeIDs = ids
	}
	if v.CPUConfig.Valid {
		cfg, err := UnmarshalCPUConfig(&v.CPUConfig.String)
		if err != nil {
			return nil, fmt.Errorf("unmarshal cpu_config: %w", err)
		}
		vm.CPUConfig = cfg
	}

	return vm, nil
}
