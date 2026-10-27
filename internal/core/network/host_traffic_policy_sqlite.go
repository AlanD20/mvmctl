package network

import (
	"context"
	"database/sql"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ProjectNetworkToHost records the ownership receipt returned by root without changing policy authority.
func (r *trafficPolicySQLiteRepo) ProjectNetworkToHost(
	ctx context.Context,
	intent *model.NetworkToHostTrafficAllowIntent,
) error {
	result, err := r.db.ExecContext(
		ctx,
		`INSERT INTO network_host_traffic_policies
 (id,source_network_id,protocol,destination_port_start,destination_port_end,created_at,updated_at,claim_id,incarnation)
 VALUES (?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET claim_id=excluded.claim_id,incarnation=excluded.incarnation,updated_at=excluded.updated_at
 WHERE network_host_traffic_policies.source_network_id=excluded.source_network_id
 AND network_host_traffic_policies.protocol=excluded.protocol AND network_host_traffic_policies.destination_port_start=excluded.destination_port_start
 AND network_host_traffic_policies.destination_port_end=excluded.destination_port_end`,
		intent.ID,
		intent.SourceNetworkID,
		intent.Protocol,
		intent.DestinationPortStart,
		intent.DestinationPortEnd,
		intent.CreatedAt,
		intent.UpdatedAt,
		intent.ClaimID,
		intent.Incarnation,
	)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errs.New(errs.CodePolicyAlreadyExists, "local policy projection identity differs from root receipt")
	}
	return nil
}

// DeleteOwnedNetworkToHost cannot delete a concurrently replaced local projection.
func (r *trafficPolicySQLiteRepo) DeleteOwnedNetworkToHost(
	ctx context.Context,
	policyID, claim, incarnation string,
) error {
	_, err := r.db.ExecContext(
		ctx,
		`DELETE FROM network_host_traffic_policies WHERE id=? AND claim_id=? AND (?='' OR incarnation=?)`,
		policyID,
		claim,
		incarnation,
		incarnation,
	)
	return err
}

// ProjectVMToHost records the ownership receipt returned by root without changing policy authority.
func (r *trafficPolicySQLiteRepo) ProjectVMToHost(ctx context.Context, intent *model.VMToHostTrafficAllowIntent) error {
	result, err := r.db.ExecContext(
		ctx,
		`INSERT INTO vm_host_traffic_policies
 (id,source_vm_id,protocol,destination_port_start,destination_port_end,created_at,updated_at,claim_id,incarnation)
 VALUES (?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET claim_id=excluded.claim_id,incarnation=excluded.incarnation,updated_at=excluded.updated_at
 WHERE vm_host_traffic_policies.source_vm_id=excluded.source_vm_id
 AND vm_host_traffic_policies.protocol=excluded.protocol AND vm_host_traffic_policies.destination_port_start=excluded.destination_port_start
 AND vm_host_traffic_policies.destination_port_end=excluded.destination_port_end`,
		intent.ID,
		intent.SourceVMID,
		intent.Protocol,
		intent.DestinationPortStart,
		intent.DestinationPortEnd,
		intent.CreatedAt,
		intent.UpdatedAt,
		intent.ClaimID,
		intent.Incarnation,
	)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errs.New(errs.CodePolicyAlreadyExists, "local policy projection identity differs from root receipt")
	}
	return nil
}

// DeleteOwnedVMToHost cannot delete a concurrently replaced local projection.
func (r *trafficPolicySQLiteRepo) DeleteOwnedVMToHost(ctx context.Context, policyID, claim, incarnation string) error {
	_, err := r.db.ExecContext(
		ctx,
		`DELETE FROM vm_host_traffic_policies WHERE id=? AND claim_id=? AND (?='' OR incarnation=?)`,
		policyID,
		claim,
		incarnation,
		incarnation,
	)
	return err
}

func (r *trafficPolicySQLiteRepo) GetVMToHost(
	ctx context.Context,
	policyID string,
) (*model.VMToHostTrafficAllowIntent, error) {
	var intent model.VMToHostTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM vm_host_traffic_policies WHERE id = ?`, policyID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) FindVMToHostByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.VMToHostTrafficAllowIntent, error) {
	var intents []*model.VMToHostTrafficAllowIntent
	err := r.db.SelectContext(
		ctx,
		&intents,
		`SELECT * FROM vm_host_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`,
		prefix+"%",
	)
	return intents, err
}

func (r *trafficPolicySQLiteRepo) ListVMToHost(ctx context.Context) ([]*model.VMToHostTrafficAllowIntent, error) {
	var intents []*model.VMToHostTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents, `SELECT * FROM vm_host_traffic_policies ORDER BY created_at, id`)
	return intents, err
}

func (r *trafficPolicySQLiteRepo) DeleteVMToHost(ctx context.Context, policyIDs []string) error {
	return deleteTrafficPolicyAllowIntents(ctx, r.db, `DELETE FROM vm_host_traffic_policies WHERE id IN (?)`, policyIDs)
}

func (r *trafficPolicySQLiteRepo) GetNetworkToHost(
	ctx context.Context,
	policyID string,
) (*model.NetworkToHostTrafficAllowIntent, error) {
	var intent model.NetworkToHostTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM network_host_traffic_policies WHERE id = ?`, policyID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) FindNetworkToHostByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.NetworkToHostTrafficAllowIntent, error) {
	var intents []*model.NetworkToHostTrafficAllowIntent
	err := r.db.SelectContext(
		ctx,
		&intents,
		`SELECT * FROM network_host_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`,
		prefix+"%",
	)
	return intents, err
}

func (r *trafficPolicySQLiteRepo) ListNetworkToHost(
	ctx context.Context,
) ([]*model.NetworkToHostTrafficAllowIntent, error) {
	var intents []*model.NetworkToHostTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents, `SELECT * FROM network_host_traffic_policies ORDER BY created_at, id`)
	return intents, err
}

func (r *trafficPolicySQLiteRepo) DeleteNetworkToHost(ctx context.Context, policyIDs []string) error {
	return deleteTrafficPolicyAllowIntents(
		ctx,
		r.db,
		`DELETE FROM network_host_traffic_policies WHERE id IN (?)`,
		policyIDs,
	)
}
