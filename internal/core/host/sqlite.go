package host

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &sqliteRepo{db: db}
}

func (r *sqliteRepo) Get(ctx context.Context) (*model.HostStateItem, error) {
	var h model.HostStateItem
	err := r.db.GetContext(ctx, &h, `SELECT * FROM host_state WHERE id = 1`)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &h, err
}

func (r *sqliteRepo) Upsert(ctx context.Context, h *model.HostStateItem) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO host_state (
			id, initialized, mvm_group_created, sudoers_configured,
			default_network_created, initialized_at, updated_at,
			hostname, cpu_model, cpu_vendor, cpu_cores, cpu_architecture,
			numa_nodes, memory_total_mib, storage_total_bytes,
			kernel_version, os_release, pid_max, fd_max, conntrack_max,
			tap_devices_max, ip_local_port_range, detected_at,
			cpu_has_vmx, cpu_hypervisor, nested_virt_available, ept_available,
			hugepage_count_2mb, ksm_disabled, cgroup_version, swap_total_mib,
			kernel_minimum_met
		) VALUES (1, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?)
		ON CONFLICT(id) DO UPDATE SET
			initialized = excluded.initialized,
			mvm_group_created = excluded.mvm_group_created,
			sudoers_configured = excluded.sudoers_configured,
			default_network_created = excluded.default_network_created,
			initialized_at = excluded.initialized_at,
			updated_at = CURRENT_TIMESTAMP,
			hostname = excluded.hostname,
			cpu_model = excluded.cpu_model,
			cpu_vendor = excluded.cpu_vendor,
			cpu_cores = excluded.cpu_cores,
			cpu_architecture = excluded.cpu_architecture,
			numa_nodes = excluded.numa_nodes,
			memory_total_mib = excluded.memory_total_mib,
			storage_total_bytes = excluded.storage_total_bytes,
			kernel_version = excluded.kernel_version,
			os_release = excluded.os_release,
			pid_max = excluded.pid_max,
			fd_max = excluded.fd_max,
			conntrack_max = excluded.conntrack_max,
			tap_devices_max = excluded.tap_devices_max,
			ip_local_port_range = excluded.ip_local_port_range,
			detected_at = excluded.detected_at,
			cpu_has_vmx = excluded.cpu_has_vmx,
			cpu_hypervisor = excluded.cpu_hypervisor,
			nested_virt_available = excluded.nested_virt_available,
			ept_available = excluded.ept_available,
			hugepage_count_2mb = excluded.hugepage_count_2mb,
			ksm_disabled = excluded.ksm_disabled,
			cgroup_version = excluded.cgroup_version,
			swap_total_mib = excluded.swap_total_mib,
			kernel_minimum_met = excluded.kernel_minimum_met`,
		infra.BoolToInt(h.Initialized), infra.BoolToInt(h.MvmGroupCreated),
		infra.BoolToInt(h.SudoersConfigured), infra.BoolToInt(h.DefaultNetworkCreated),
		h.InitializedAt, h.UpdatedAt,
		h.Hostname, h.CPUModel, h.CPUVendor, h.CPUCores, h.CPUArchitecture,
		h.NumaNodes, h.MemoryTotalMiB, h.StorageTotalBytes,
		h.KernelVersion, h.OSRelease, h.PIDMax, h.FDMax, h.ConntrackMax,
		h.TAPDevicesMax, h.IPLocalPortRange, h.DetectedAt,
		h.CPUHasVMX, h.CPUHypervisor, h.NestedVirtAvailable, h.EPTAvailable,
		h.HugepageCount2MB, h.KSMDisabled, h.CgroupVersion, h.SwapTotalMiB,
		h.KernelMinimumMet,
	)
	return err
}

func (r *sqliteRepo) GetChangesBySession(ctx context.Context, sessionID string) ([]*model.HostStateChangeItem, error) {
	var items []*model.HostStateChangeItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM host_state_changes WHERE session_id = ? ORDER BY change_order`, sessionID)
}

func (r *sqliteRepo) GetLatestSessionChanges(ctx context.Context) ([]*model.HostStateChangeItem, error) {
	var items []*model.HostStateChangeItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM host_state_changes
		WHERE session_id = (SELECT session_id FROM host_state_changes ORDER BY created_at DESC LIMIT 1)
		ORDER BY change_order`)
}

func (r *sqliteRepo) AddChange(ctx context.Context, change *model.HostStateChangeItem) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO host_state_changes (
			session_id, init_timestamp, setting, mechanism,
			original_value, applied_value, reverted, reverted_at,
			revert_mechanism, change_order, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		change.SessionID, change.InitTimestamp, change.Setting, change.Mechanism,
		change.OriginalValue, change.AppliedValue, infra.BoolToInt(change.Reverted),
		change.RevertedAt, change.RevertMechanism, change.ChangeOrder, change.CreatedAt,
	)
	return err
}

func (r *sqliteRepo) MarkSessionReverted(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE host_state_changes SET reverted = 1 WHERE session_id = ?`, sessionID)
	return err
}

func (r *sqliteRepo) GetState(ctx context.Context) (*model.HostStateItem, error) {
	var h model.HostStateItem
	err := r.db.GetContext(ctx, &h, `SELECT * FROM host_state WHERE id = 1`)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &h, err
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	return count, sqlx.GetContext(ctx, r.db, &count, `SELECT COUNT(*) FROM host_state_changes`)
}

func (r *sqliteRepo) InitializeState(ctx context.Context) (*model.HostStateItem, error) {
	_, err := r.db.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO host_state (id, initialized_at, updated_at) VALUES (1, '', '')`,
	)
	if err != nil {
		return nil, err
	}
	return r.GetState(ctx)
}

func (r *sqliteRepo) SetInitialized(ctx context.Context, initializedAt string) error {
	_, err := r.db.ExecContext(
		ctx,
		`UPDATE host_state SET initialized = 1, initialized_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`,
		initializedAt,
	)
	return err
}

func (r *sqliteRepo) UpdateComponent(ctx context.Context, component string, value bool) error {
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE host_state SET %s = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, component),
		infra.BoolToInt(value))
	return err
}

func (r *sqliteRepo) ResetState(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM host_state WHERE id = 1`)
	return err
}

func (r *sqliteRepo) SaveCapacity(ctx context.Context,
	hostname string,
	cpuModel string,
	cpuVendor string,
	cpuCores int,
	cpuArchitecture string,
	numaNodes int,
	memoryTotalMiB int,
	storageTotalBytes int,
	kernelVersion string,
	osRelease string,
	pidMax int,
	fdMax int,
	conntrackMax int,
	tapDevicesMax int,
	ipLocalPortRange [2]int,
	detectedAt string,
	cpuHasVMX bool,
	cpuHypervisor bool,
	nestedVirtAvailable bool,
	eptAvailable bool,
	hugepageCount2MB int,
	ksmDisabled bool,
	cgroupVersion int,
	swapTotalMiB int,
	kernelMinimumMet bool,
) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE host_state SET
			hostname = ?, cpu_model = ?, cpu_vendor = ?, cpu_cores = ?, cpu_architecture = ?,
			numa_nodes = ?, memory_total_mib = ?, storage_total_bytes = ?,
			kernel_version = ?, os_release = ?, pid_max = ?, fd_max = ?,
			conntrack_max = ?, tap_devices_max = ?,
			ip_local_port_range = ?, detected_at = ?,
			cpu_has_vmx = ?, cpu_hypervisor = ?, nested_virt_available = ?,
			ept_available = ?, hugepage_count_2mb = ?, ksm_disabled = ?,
			cgroup_version = ?, swap_total_mib = ?, kernel_minimum_met = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = 1`,
		hostname, cpuModel, cpuVendor, cpuCores, cpuArchitecture,
		numaNodes, memoryTotalMiB, storageTotalBytes,
		kernelVersion, osRelease, pidMax, fdMax,
		conntrackMax, tapDevicesMax,
		ipLocalPortRange[0], detectedAt,
		infra.BoolToInt(cpuHasVMX), infra.BoolToInt(cpuHypervisor),
		infra.BoolToInt(nestedVirtAvailable), infra.BoolToInt(eptAvailable),
		hugepageCount2MB, infra.BoolToInt(ksmDisabled),
		cgroupVersion, swapTotalMiB, infra.BoolToInt(kernelMinimumMet),
	)
	return err
}

func (r *sqliteRepo) AddChanges(ctx context.Context, changes []*model.HostStateChangeItem) error {
	for _, c := range changes {
		if err := r.AddChange(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func (r *sqliteRepo) DeleteChangesExceptSession(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM host_state_changes WHERE session_id != ?`, sessionID)
	return err
}

func (r *sqliteRepo) ListChanges(
	ctx context.Context,
	sessionID *string,
	includeReverted bool,
) ([]*model.HostStateChangeItem, error) {
	var items []*model.HostStateChangeItem
	if sessionID != nil {
		if includeReverted {
			return items, r.db.SelectContext(ctx, &items,
				`SELECT * FROM host_state_changes WHERE session_id = ? ORDER BY change_order`, *sessionID)
		}
		return items, r.db.SelectContext(ctx, &items,
			`SELECT * FROM host_state_changes WHERE session_id = ? AND reverted = 0 ORDER BY change_order`, *sessionID)
	}
	if includeReverted {
		return items, r.db.SelectContext(ctx, &items,
			`SELECT * FROM host_state_changes ORDER BY change_order`)
	}
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM host_state_changes WHERE reverted = 0 ORDER BY change_order`)
}

func (r *sqliteRepo) MarkChangeReverted(
	ctx context.Context,
	changeID int,
	revertedAt string,
	revertMechanism *string,
) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE host_state_changes SET reverted = 1, reverted_at = ?, revert_mechanism = ? WHERE id = ?`,
		revertedAt, revertMechanism, changeID)
	return err
}

func (r *sqliteRepo) RevertChanges(
	ctx context.Context,
	sessionID string,
	revertedAt string,
) ([]*model.HostStateChangeItem, error) {
	items, err := r.ListChanges(ctx, &sessionID, false)
	if err != nil {
		return nil, err
	}
	for i := len(items) - 1; i >= 0; i-- {
		if err := r.MarkChangeReverted(ctx, *items[i].ID, revertedAt, nil); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (r *sqliteRepo) HasUnrevertedChanges(ctx context.Context) (bool, error) {
	var count int
	err := sqlx.GetContext(ctx, r.db, &count,
		`SELECT COUNT(*) FROM host_state_changes WHERE reverted = 0`)
	return count > 0, err
}
