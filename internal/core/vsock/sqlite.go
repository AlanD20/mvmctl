package vsock

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

// NewRepository creates a new SQLite-backed vsock repository.
func NewRepository(db *sqlx.DB) Repository {
	return &sqliteRepo{db: db}
}

const vsockBaseQuery = "SELECT * FROM vm_vsock_config"

// GetByVMID returns the vsock config for a VM. Returns nil, nil if not found.
func (r *sqliteRepo) GetByVMID(ctx context.Context, vmID string) (*model.VsockConfigItem, error) {
	var item model.VsockConfigItem
	err := sqlx.GetContext(ctx, r.db, &item, vsockBaseQuery+" WHERE vm_id = ?", vmID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &item, err
}

// Upsert creates or updates a vsock config record.
func (r *sqliteRepo) Upsert(ctx context.Context, item *model.VsockConfigItem) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO vm_vsock_config (id, vm_id, guest_cid, uds_path, port, token)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(vm_id) DO UPDATE SET
			guest_cid = excluded.guest_cid,
			uds_path = excluded.uds_path,
			port = excluded.port,
			token = excluded.token
	`,
		item.ID,
		item.VmID,
		item.GuestCID,
		item.UDSPath,
		item.Port,
		item.Token,
	)
	return err
}

// ListByVMIDs returns vsock configs for multiple VMs. Returns empty slice if none found.
func (r *sqliteRepo) ListByVMIDs(ctx context.Context, vmIDs []string) ([]*model.VsockConfigItem, error) {
	if len(vmIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(vsockBaseQuery+" WHERE vm_id IN (?)", vmIDs)
	if err != nil {
		return nil, err
	}
	query = r.db.Rebind(query)
	var items []*model.VsockConfigItem
	if err := r.db.SelectContext(ctx, &items, query, args...); err != nil {
		return nil, err
	}
	return items, nil
}

// DeleteByVMID removes the vsock config for a VM. No-op if not found.
func (r *sqliteRepo) DeleteByVMID(ctx context.Context, vmID string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM vm_vsock_config WHERE vm_id = ?", vmID)
	return err
}

// SetUpgradeLock sets the upgrade lock for a VM. Returns error if lock already held.
func (r *sqliteRepo) SetUpgradeLock(ctx context.Context, vmID string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE vm_vsock_config SET upgrading = 1,
		 upgrade_started_at = datetime('now')
		 WHERE vm_id = ? AND upgrading = 0`,
		vmID,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("upgrade already in progress for VM %s", vmID)
	}
	return nil
}

// ClearUpgradeLock removes the upgrade lock for a VM.
func (r *sqliteRepo) ClearUpgradeLock(ctx context.Context, vmID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE vm_vsock_config SET upgrading = 0,
		 upgrade_started_at = NULL
		 WHERE vm_id = ?`,
		vmID,
	)
	return err
}

// UpdateAgentVersion persists the agent version after a successful upgrade.
func (r *sqliteRepo) UpdateAgentVersion(ctx context.Context, vmID, version string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE vm_vsock_config SET agent_version = ? WHERE vm_id = ?`,
		version, vmID,
	)
	return err
}
