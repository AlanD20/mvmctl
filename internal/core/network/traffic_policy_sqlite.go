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

func (r *trafficPolicySQLiteRepo) CreateFromNetwork(
	ctx context.Context,
	intent *model.NetworkTrafficAllowIntent,
) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO network_traffic_policies
		(id, source_network_id, destination_vm_id, protocol, destination_port_start,
		 destination_port_end, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.ID, intent.SourceNetworkID, intent.DestinationVMID, string(intent.Protocol),
		intent.DestinationPortStart, intent.DestinationPortEnd, intent.CreatedAt, intent.UpdatedAt)
	return err
}

func (r *trafficPolicySQLiteRepo) GetFromNetwork(
	ctx context.Context,
	intentID string,
) (*model.NetworkTrafficAllowIntent, error) {
	var intent model.NetworkTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM network_traffic_policies WHERE id = ?`, intentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) GetNetworkByIdentity(
	ctx context.Context,
	identity *model.NetworkTrafficAllowIntent,
) (*model.NetworkTrafficAllowIntent, error) {
	var intent model.NetworkTrafficAllowIntent
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

func (r *trafficPolicySQLiteRepo) FindNetworksByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.NetworkTrafficAllowIntent, error) {
	var intents []*model.NetworkTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents,
		`SELECT * FROM network_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return intents, err
}

func (r *trafficPolicySQLiteRepo) ListFromNetworks(
	ctx context.Context,
) ([]*model.NetworkTrafficAllowIntent, error) {
	var intents []*model.NetworkTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents, `SELECT * FROM network_traffic_policies ORDER BY created_at, id`)
	return intents, err
}

func (r *trafficPolicySQLiteRepo) DeleteFromNetworks(ctx context.Context, intentIDs []string) error {
	return deleteTrafficPolicyAllowIntents(ctx, r.db, `DELETE FROM network_traffic_policies WHERE id IN (?)`, intentIDs)
}

func (r *trafficPolicySQLiteRepo) CreateFromVM(ctx context.Context, intent *model.VMTrafficAllowIntent) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO vm_traffic_policies
		(id, source_vm_id, destination_vm_id, protocol, destination_port_start,
		 destination_port_end, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.ID, intent.SourceVMID, intent.DestinationVMID, string(intent.Protocol),
		intent.DestinationPortStart, intent.DestinationPortEnd, intent.CreatedAt, intent.UpdatedAt)
	return err
}

func (r *trafficPolicySQLiteRepo) GetFromVM(
	ctx context.Context,
	intentID string,
) (*model.VMTrafficAllowIntent, error) {
	var intent model.VMTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM vm_traffic_policies WHERE id = ?`, intentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) GetVMByIdentity(
	ctx context.Context,
	identity *model.VMTrafficAllowIntent,
) (*model.VMTrafficAllowIntent, error) {
	var intent model.VMTrafficAllowIntent
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

func (r *trafficPolicySQLiteRepo) FindVMsByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.VMTrafficAllowIntent, error) {
	var intents []*model.VMTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents,
		`SELECT * FROM vm_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return intents, err
}

func (r *trafficPolicySQLiteRepo) ListFromVMs(ctx context.Context) ([]*model.VMTrafficAllowIntent, error) {
	var intents []*model.VMTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents, `SELECT * FROM vm_traffic_policies ORDER BY created_at, id`)
	return intents, err
}

func (r *trafficPolicySQLiteRepo) DeleteFromVMs(ctx context.Context, intentIDs []string) error {
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
