package execpolicy

import (
	"context"
	"database/sql"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// EnsureOwned inserts or observes one policy and ownership receipt in a single transaction.
func (r *sqliteRepo) EnsureOwned(
	ctx context.Context,
	intent *model.VMToVMExecAllowIntent,
	expected string,
) (*model.VMToVMExecAllowIntent, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() // Commit owns the result; rollback after commit is harmless.
	if expected == "" {
		incarnation := crypto.UUIDV4()
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO exec_policies
   (id,source_vm_id,destination_vm_id,target_user,created_at,updated_at,claim_id,incarnation)
   VALUES (?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			intent.ID,
			intent.SourceVMID,
			intent.DestinationVMID,
			intent.TargetUser,
			intent.CreatedAt,
			intent.UpdatedAt,
			intent.ClaimID,
			incarnation,
		)
		if err != nil {
			return nil, err
		}
	}
	var existing model.VMToVMExecAllowIntent
	err = tx.GetContext(ctx, &existing, `SELECT * FROM exec_policies WHERE id=?`, intent.ID)
	if err == sql.ErrNoRows {
		return nil, errs.New(errs.CodePolicyAlreadyExists, "recorded execution policy is absent")
	}
	if err != nil {
		return nil, err
	}
	if existing.SourceVMID != intent.SourceVMID || existing.DestinationVMID != intent.DestinationVMID ||
		existing.TargetUser != intent.TargetUser ||
		(expected != "" && existing.Incarnation != expected) {
		return nil, errs.New(errs.CodePolicyAlreadyExists, "execution policy identity or incarnation changed")
	}
	if existing.Incarnation == "" {
		existing.Incarnation = crypto.UUIDV4()
		if _, err = tx.ExecContext(
			ctx,
			`UPDATE exec_policies SET incarnation=? WHERE id=?`,
			existing.Incarnation,
			existing.ID,
		); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &existing, nil
}

// DeleteOwned atomically refuses replacement or foreign ownership and accepts exact absence.
func (r *sqliteRepo) DeleteOwned(ctx context.Context, policyID, claim, incarnation string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // Commit owns the result.
	var existing model.VMToVMExecAllowIntent
	err = tx.GetContext(ctx, &existing, `SELECT * FROM exec_policies WHERE id=?`, policyID)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if existing.ClaimID != claim || (incarnation != "" && existing.Incarnation != incarnation) {
		return errs.New(errs.CodePolicyAlreadyExists, "execution policy ownership changed")
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM exec_policies WHERE id=? AND claim_id=? AND incarnation=?`,
		policyID, claim, existing.Incarnation); err != nil {
		return err
	}
	return tx.Commit()
}
