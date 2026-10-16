package network

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

type trafficPolicySQLiteRepo struct{ db *sqlx.DB }

// NewTrafficPolicyRepository creates the SQLite traffic allow-intent repository.
func NewTrafficPolicyRepository(db *sqlx.DB) TrafficPolicyRepository {
	return &trafficPolicySQLiteRepo{db: db}
}

func (r *trafficPolicySQLiteRepo) CreateNetworkToVM(
	ctx context.Context,
	intent *model.NetworkToVMTrafficAllowIntent,
) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO network_traffic_policies
		(id, source_network_id, destination_vm_id, protocol, destination_port_start,
		 destination_port_end, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.ID, intent.SourceNetworkID, intent.DestinationVMID, string(intent.Protocol),
		intent.DestinationPortStart, intent.DestinationPortEnd, intent.CreatedAt, intent.UpdatedAt)
	return err
}

func (r *trafficPolicySQLiteRepo) GetNetworkToVM(
	ctx context.Context,
	intentID string,
) (*model.NetworkToVMTrafficAllowIntent, error) {
	var intent model.NetworkToVMTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM network_traffic_policies WHERE id = ?`, intentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) GetNetworkByIdentity(
	ctx context.Context,
	identity *model.NetworkToVMTrafficAllowIntent,
) (*model.NetworkToVMTrafficAllowIntent, error) {
	var intent model.NetworkToVMTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM network_traffic_policies
		WHERE source_network_id = ? AND destination_vm_id = ? AND protocol = ?
		AND destination_port_start = ? AND destination_port_end = ?`,
		identity.SourceNetworkID, identity.DestinationVMID, string(identity.Protocol),
		identity.DestinationPortStart, identity.DestinationPortEnd)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) FindNetworkToVMByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.NetworkToVMTrafficAllowIntent, error) {
	var intents []*model.NetworkToVMTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents,
		`SELECT * FROM network_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return intents, err
}

func (r *trafficPolicySQLiteRepo) ListNetworkToVM(
	ctx context.Context,
) ([]*model.NetworkToVMTrafficAllowIntent, error) {
	var intents []*model.NetworkToVMTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents, `SELECT * FROM network_traffic_policies ORDER BY created_at, id`)
	return intents, err
}

func (r *trafficPolicySQLiteRepo) DeleteNetworkToVM(ctx context.Context, intentIDs []string) error {
	return deleteTrafficPolicyAllowIntents(ctx, r.db, `DELETE FROM network_traffic_policies WHERE id IN (?)`, intentIDs)
}

func (r *trafficPolicySQLiteRepo) CreateVMToVM(ctx context.Context, intent *model.VMToVMTrafficAllowIntent) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO vm_traffic_policies
		(id, source_vm_id, destination_vm_id, protocol, destination_port_start,
		 destination_port_end, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.ID, intent.SourceVMID, intent.DestinationVMID, string(intent.Protocol),
		intent.DestinationPortStart, intent.DestinationPortEnd, intent.CreatedAt, intent.UpdatedAt)
	return err
}

func (r *trafficPolicySQLiteRepo) GetVMToVM(
	ctx context.Context,
	intentID string,
) (*model.VMToVMTrafficAllowIntent, error) {
	var intent model.VMToVMTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM vm_traffic_policies WHERE id = ?`, intentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) GetVMByIdentity(
	ctx context.Context,
	identity *model.VMToVMTrafficAllowIntent,
) (*model.VMToVMTrafficAllowIntent, error) {
	var intent model.VMToVMTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM vm_traffic_policies
		WHERE source_vm_id = ? AND destination_vm_id = ? AND protocol = ?
		AND destination_port_start = ? AND destination_port_end = ?`,
		identity.SourceVMID, identity.DestinationVMID, string(identity.Protocol),
		identity.DestinationPortStart, identity.DestinationPortEnd)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) FindVMToVMByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.VMToVMTrafficAllowIntent, error) {
	var intents []*model.VMToVMTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents,
		`SELECT * FROM vm_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return intents, err
}

func (r *trafficPolicySQLiteRepo) ListVMToVM(ctx context.Context) ([]*model.VMToVMTrafficAllowIntent, error) {
	var intents []*model.VMToVMTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents, `SELECT * FROM vm_traffic_policies ORDER BY created_at, id`)
	return intents, err
}

func (r *trafficPolicySQLiteRepo) DeleteVMToVM(ctx context.Context, intentIDs []string) error {
	return deleteTrafficPolicyAllowIntents(ctx, r.db, `DELETE FROM vm_traffic_policies WHERE id IN (?)`, intentIDs)
}

func (r *trafficPolicySQLiteRepo) InvalidateByVM(ctx context.Context, vmID string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		// Rollback after Commit returns sql.ErrTxDone; the operation result is authoritative.
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM network_traffic_policies WHERE destination_vm_id = ?`, vmID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM vm_traffic_policies WHERE source_vm_id = ? OR destination_vm_id = ?`, vmID, vmID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *trafficPolicySQLiteRepo) InvalidateByNetwork(ctx context.Context, networkID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM network_traffic_policies WHERE source_network_id = ?`, networkID)
	return err
}

func deleteTrafficPolicyAllowIntents(
	ctx context.Context,
	database *sqlx.DB,
	statement string,
	intentIDs []string,
) error {
	if len(intentIDs) == 0 {
		return nil
	}
	query, args, err := sqlx.In(statement, intentIDs)
	if err != nil {
		return err
	}
	tx, err := database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		// Rollback after Commit returns sql.ErrTxDone; the operation result is authoritative.
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, database.Rebind(query), args...); err != nil {
		return err
	}
	return tx.Commit()
}
