package host

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

type sqliteRepo struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository {
	return &sqliteRepo{db: db}
}

// ── Count ──
// Matches Python's Repository.count().
func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	row := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM host_state_changes")
	var count int
	err := row.Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// ── GetState ──
// Matches Python's Repository.get_state().
func (r *sqliteRepo) GetState(ctx context.Context) (*model.HostStateItem, error) {
	row := r.db.QueryRowContext(ctx, "SELECT * FROM host_state WHERE id = 1")
	return scanHostState(row)
}

// ── InitializeState ──
// Matches Python's Repository.initialize_state().
func (r *sqliteRepo) InitializeState(ctx context.Context) (*model.HostStateItem, error) {
	_, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO host_state
		(id, initialized, mvm_group_created, sudoers_configured, default_network_created, initialized_at, updated_at)
		VALUES (1, 0, 0, 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`)
	if err != nil {
		return nil, err
	}
	state, err := r.GetState(ctx)
	if err != nil {
		return nil, err
	}
	// Python asserts host_state is not None after INSERT OR IGNORE
	if state == nil {
		return nil, fmt.Errorf("host state row id=1 not found after insert")
	}
	return state, nil
}

// ── SetInitialized ──
// Matches Python's Repository.set_initialized().
func (r *sqliteRepo) SetInitialized(ctx context.Context, initializedAt string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE host_state
		SET initialized = 1, initialized_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`, initializedAt)
	return err
}

// ── UpdateComponent ──
// Matches Python's Repository.update_component().
// Python raises ValueError with repr() formatting; Go uses standard %q which
// matches Python's repr() behavior for strings (adds quotes and escapes).
func (r *sqliteRepo) UpdateComponent(ctx context.Context, component string, value bool) error {
	allowed := map[string]bool{
		"mvm_group_created":       true,
		"sudoers_configured":      true,
		"default_network_created": true,
	}
	if !allowed[component] {
		return fmt.Errorf("Unknown host state component: %q", component)
	}
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf("UPDATE host_state SET %s = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1", component),
		infra.BoolToInt(value),
	)
	return err
}

// ── ResetState ──
// Matches Python's Repository.reset_state().
func (r *sqliteRepo) ResetState(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE host_state SET
			initialized = 0,
			mvm_group_created = 0,
			sudoers_configured = 0,
			default_network_created = 0,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`)
	return err
}

// ── SaveCapacity ──
// Matches Python's Repository.save_capacity() exactly.
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
	portRangeStr := fmt.Sprintf("%d,%d", ipLocalPortRange[0], ipLocalPortRange[1])
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Ensure row exists (singleton id=1)
	_, err = tx.Exec(`
		INSERT OR IGNORE INTO host_state
		(id, initialized, initialized_at, updated_at)
		VALUES (1, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		UPDATE host_state SET
			hostname = ?,
			cpu_model = ?,
			cpu_vendor = ?,
			cpu_cores = ?,
			cpu_architecture = ?,
			numa_nodes = ?,
			memory_total_mib = ?,
			storage_total_bytes = ?,
			kernel_version = ?,
			os_release = ?,
			pid_max = ?,
			fd_max = ?,
			conntrack_max = ?,
			tap_devices_max = ?,
			ip_local_port_range = ?,
			detected_at = ?,
			cpu_has_vmx = ?,
			cpu_hypervisor = ?,
			nested_virt_available = ?,
			ept_available = ?,
			hugepage_count_2mb = ?,
			ksm_disabled = ?,
			cgroup_version = ?,
			swap_total_mib = ?,
			kernel_minimum_met = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`,
		hostname,
		cpuModel,
		cpuVendor,
		cpuCores,
		cpuArchitecture,
		numaNodes,
		memoryTotalMiB,
		storageTotalBytes,
		kernelVersion,
		osRelease,
		pidMax,
		fdMax,
		conntrackMax,
		tapDevicesMax,
		portRangeStr,
		detectedAt,
		infra.BoolToInt(cpuHasVMX),
		infra.BoolToInt(cpuHypervisor),
		infra.BoolToInt(nestedVirtAvailable),
		infra.BoolToInt(eptAvailable),
		hugepageCount2MB,
		infra.BoolToInt(ksmDisabled),
		cgroupVersion,
		swapTotalMiB,
		infra.BoolToInt(kernelMinimumMet),
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// ── AddChange ──
// Matches Python's Repository.add_change().
func (r *sqliteRepo) AddChange(ctx context.Context, change *model.HostStateChangeItem) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO host_state_changes (
			session_id, init_timestamp, setting, mechanism,
			original_value, applied_value, reverted, reverted_at,
			revert_mechanism, change_order, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`,
		change.SessionID,
		change.InitTimestamp,
		change.Setting,
		change.Mechanism,
		change.OriginalValue,
		change.AppliedValue,
		infra.BoolToInt(change.Reverted),
		change.RevertedAt,
		change.RevertMechanism,
		change.ChangeOrder,
	)
	return err
}

// ── AddChanges ──
// Matches Python's Repository.add_changes() — bulk insert in a single transaction.
func (r *sqliteRepo) AddChanges(ctx context.Context, changes []*model.HostStateChangeItem) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, change := range changes {
		_, err := tx.Exec(`
			INSERT INTO host_state_changes (
				session_id, init_timestamp, setting, mechanism,
				original_value, applied_value, reverted, reverted_at,
				revert_mechanism, change_order, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			change.SessionID,
			change.InitTimestamp,
			change.Setting,
			change.Mechanism,
			change.OriginalValue,
			change.AppliedValue,
			infra.BoolToInt(change.Reverted),
			change.RevertedAt,
			change.RevertMechanism,
			change.ChangeOrder,
			change.CreatedAt,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ── DeleteChangesExceptSession ──
// Matches Python's Repository.delete_changes_except_session().
func (r *sqliteRepo) DeleteChangesExceptSession(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM host_state_changes WHERE session_id != ?",
		sessionID,
	)
	return err
}

// ── ListChanges ──
// Matches Python's Repository.list_changes().
func (r *sqliteRepo) ListChanges(ctx context.Context, sessionID *string, includeReverted bool) ([]*model.HostStateChangeItem, error) {
	query := "SELECT * FROM host_state_changes"
	var params []interface{}
	var conditions []string

	if sessionID != nil {
		conditions = append(conditions, "session_id = ?")
		params = append(params, *sessionID)
	}
	if !includeReverted {
		conditions = append(conditions, "reverted = 0")
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY change_order ASC"

	rows, err := r.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanHostStateChanges(rows)
}

// ── MarkChangeReverted ──
// Matches Python's Repository.mark_change_reverted().
func (r *sqliteRepo) MarkChangeReverted(ctx context.Context, changeID int, revertedAt string, revertMechanism *string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE host_state_changes
		SET reverted = 1, reverted_at = ?, revert_mechanism = ?
		WHERE id = ?
	`, revertedAt, revertMechanism, changeID)
	return err
}

// ── RevertChanges ──
// Matches Python's Repository.revert_changes() exactly.
// Python iterates in REVERSE order (LIFO) for marking, and returns reversed changes.
func (r *sqliteRepo) RevertChanges(ctx context.Context, sessionID string, revertedAt string) ([]*model.HostStateChangeItem, error) {
	changes, err := r.ListChanges(ctx, &sessionID, false)
	if err != nil {
		return nil, err
	}
	// Python: for change in reversed(changes) — LIFO order for marking.
	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		if change.ID != nil {
			err := r.MarkChangeReverted(ctx, *change.ID, revertedAt, nil)
			if err != nil {
				return nil, err
			}
		}
	}
	// Return in reversed order (LIFO), matching Python's list(reversed(changes)).
	reversed := make([]*model.HostStateChangeItem, len(changes))
	for i, c := range changes {
		reversed[len(changes)-1-i] = c
	}
	return reversed, nil
}

// ── Scan helpers ──

func scanHostState(row *sql.Row) (*model.HostStateItem, error) {
	var s model.HostStateItem
	var hostname, cpuModel, cpuVendor, cpuArch, kernelVer, osRelease, ipRange, detectedAt sql.NullString
	var cpuCores, numaNodes, pidMax, fdMax, conntrackMax, tapDevices, cpuVMX, cpuHyper sql.NullInt64
	var nestedVirt, ept, hugepages, ksm, cgroupVer, swapMiB, kernelMin sql.NullInt64
	var memMiB, storageBytes sql.NullInt64

	err := row.Scan(
		&s.ID,
		&s.Initialized, &s.MvmGroupCreated, &s.SudoersConfigured, &s.DefaultNetworkCreated,
		&s.InitializedAt, &s.UpdatedAt,
		&hostname, &cpuModel, &cpuVendor, &cpuCores, &cpuArch, &numaNodes,
		&memMiB, &storageBytes, &kernelVer, &osRelease,
		&pidMax, &fdMax, &conntrackMax, &tapDevices, &ipRange, &detectedAt,
		&cpuVMX, &cpuHyper, &nestedVirt, &ept, &hugepages, &ksm, &cgroupVer, &swapMiB, &kernelMin,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan host state: %w", err)
	}

	if hostname.Valid {
		s.Hostname = &hostname.String
	}
	if cpuModel.Valid {
		s.CPUModel = &cpuModel.String
	}
	if cpuVendor.Valid {
		s.CPUVendor = &cpuVendor.String
	}
	if cpuCores.Valid {
		v := int(cpuCores.Int64)
		s.CPUCores = &v
	}
	if cpuArch.Valid {
		s.CPUArchitecture = &cpuArch.String
	}
	if numaNodes.Valid {
		v := int(numaNodes.Int64)
		s.NumaNodes = &v
	}
	if memMiB.Valid {
		v := int(memMiB.Int64)
		s.MemoryTotalMiB = &v
	}
	if storageBytes.Valid {
		v := int(storageBytes.Int64)
		s.StorageTotalBytes = &v
	}
	if kernelVer.Valid {
		s.KernelVersion = &kernelVer.String
	}
	if osRelease.Valid {
		s.OSRelease = &osRelease.String
	}
	if pidMax.Valid {
		v := int(pidMax.Int64)
		s.PIDMax = &v
	}
	if fdMax.Valid {
		v := int(fdMax.Int64)
		s.FDMax = &v
	}
	if conntrackMax.Valid {
		v := int(conntrackMax.Int64)
		s.ConntrackMax = &v
	}
	if tapDevices.Valid {
		v := int(tapDevices.Int64)
		s.TAPDevicesMax = &v
	}
	if ipRange.Valid {
		s.IPLocalPortRange = &ipRange.String
	}
	if detectedAt.Valid {
		s.DetectedAt = &detectedAt.String
	}
	if cpuVMX.Valid {
		v := int(cpuVMX.Int64)
		s.CPUHasVMX = &v
	}
	if cpuHyper.Valid {
		v := int(cpuHyper.Int64)
		s.CPUHypervisor = &v
	}
	if nestedVirt.Valid {
		v := int(nestedVirt.Int64)
		s.NestedVirtAvailable = &v
	}
	if ept.Valid {
		v := int(ept.Int64)
		s.EPTAvailable = &v
	}
	if hugepages.Valid {
		v := int(hugepages.Int64)
		s.HugepageCount2MB = &v
	}
	if ksm.Valid {
		v := int(ksm.Int64)
		s.KSMDisabled = &v
	}
	if cgroupVer.Valid {
		v := int(cgroupVer.Int64)
		s.CgroupVersion = &v
	}
	if swapMiB.Valid {
		v := int(swapMiB.Int64)
		s.SwapTotalMiB = &v
	}
	if kernelMin.Valid {
		v := int(kernelMin.Int64)
		s.KernelMinimumMet = &v
	}

	return &s, nil
}

func scanHostStateChanges(rows *sql.Rows) ([]*model.HostStateChangeItem, error) {
	var changes []*model.HostStateChangeItem
	for rows.Next() {
		var c model.HostStateChangeItem
		var id, reverted int
		var origVal, revAt, revMech sql.NullString
		err := rows.Scan(
			&id, &c.SessionID, &c.InitTimestamp, &c.Setting, &c.Mechanism,
			&origVal, &c.AppliedValue, &reverted, &revAt, &revMech,
			&c.ChangeOrder, &c.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan change: %w", err)
		}
		c.Reverted = reverted == 1
		if origVal.Valid {
			c.OriginalValue = &origVal.String
		}
		if revAt.Valid {
			c.RevertedAt = &revAt.String
		}
		if revMech.Valid {
			c.RevertMechanism = &revMech.String
		}
		c.ID = &id
		changes = append(changes, &c)
	}
	return changes, rows.Err()
}
