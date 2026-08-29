package execpolicy

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

type sqliteRepo struct{ db *sqlx.DB }

// NewRepository creates the SQLite execution-policy repository.
func NewRepository(db *sqlx.DB) Repository { return &sqliteRepo{db: db} }

func (r *sqliteRepo) Create(ctx context.Context, intent *model.ExecAllowIntent) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO exec_policies
		(id, source_vm_id, destination_vm_id, target_user, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, intent.ID, intent.SourceVMID, intent.DestinationVMID,
		intent.TargetUser, intent.CreatedAt, intent.UpdatedAt)
	return err
}

func (r *sqliteRepo) Get(ctx context.Context, intentID string) (*model.ExecAllowIntent, error) {
	var intent model.ExecAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM exec_policies WHERE id = ?`, intentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *sqliteRepo) GetByIdentity(
	ctx context.Context,
	identity *model.ExecAllowIntent,
) (*model.ExecAllowIntent, error) {
	var intent model.ExecAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM exec_policies
		WHERE source_vm_id = ? AND destination_vm_id = ? AND target_user = ?`,
		identity.SourceVMID, identity.DestinationVMID, identity.TargetUser)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.ExecAllowIntent, error) {
	var intents []*model.ExecAllowIntent
	err := r.db.SelectContext(ctx, &intents,
		`SELECT * FROM exec_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return intents, err
}

func (r *sqliteRepo) List(ctx context.Context) ([]*model.ExecAllowIntent, error) {
	var intents []*model.ExecAllowIntent
	err := r.db.SelectContext(ctx, &intents, `SELECT * FROM exec_policies ORDER BY created_at, id`)
	return intents, err
}

func (r *sqliteRepo) DeleteMany(ctx context.Context, policyIDs []string) error {
	if len(policyIDs) == 0 {
		return nil
	}
	query, args, err := sqlx.In(`DELETE FROM exec_policies WHERE id IN (?)`, policyIDs)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		// Rollback after Commit returns sql.ErrTxDone; the operation result is authoritative.
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, r.db.Rebind(query), args...); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *sqliteRepo) DeleteByVM(ctx context.Context, vmID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM exec_policies WHERE source_vm_id = ? OR destination_vm_id = ?`, vmID, vmID)
	return err
}

func (r *sqliteRepo) Allows(
	ctx context.Context,
	sourceVMID string,
	destinationVMID string,
	targetUser string,
) (bool, error) {
	var count int
	err := r.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM exec_policies
		WHERE source_vm_id = ? AND destination_vm_id = ? AND target_user = ?`,
		sourceVMID, destinationVMID, targetUser)
	return count == 1, err
}
