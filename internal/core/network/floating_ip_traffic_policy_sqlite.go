package network

import (
	"context"
	"database/sql"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ProjectVMToFloatingIP records the ownership receipt returned by root without changing policy authority.
func (r *trafficPolicySQLiteRepo) ProjectVMToFloatingIP(
	ctx context.Context,
	intent *model.VMToFloatingIPTrafficAllowIntent,
) error {
	result, err := r.db.ExecContext(
		ctx,
		`INSERT INTO vm_floating_ip_traffic_policies
 (id,source_vm_id,destination_floating_ip_id,protocol,destination_port_start,destination_port_end,
 created_at,updated_at,claim_id,incarnation)
 VALUES (?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET claim_id=excluded.claim_id,
 incarnation=excluded.incarnation,updated_at=excluded.updated_at
 WHERE vm_floating_ip_traffic_policies.source_vm_id=excluded.source_vm_id
 AND vm_floating_ip_traffic_policies.destination_floating_ip_id=excluded.destination_floating_ip_id

 AND vm_floating_ip_traffic_policies.protocol=excluded.protocol
 AND vm_floating_ip_traffic_policies.destination_port_start=excluded.destination_port_start
 AND vm_floating_ip_traffic_policies.destination_port_end=excluded.destination_port_end`,
		intent.ID,
		intent.SourceVMID,
		intent.DestinationFloatingIPID,
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

// DeleteOwnedVMToFloatingIP cannot delete a concurrently replaced local projection.
func (r *trafficPolicySQLiteRepo) DeleteOwnedVMToFloatingIP(
	ctx context.Context,
	policyID,
	claim,
	incarnation string,
) error {
	_, err := r.db.ExecContext(
		ctx,
		`DELETE FROM vm_floating_ip_traffic_policies WHERE id=? AND claim_id=? AND (?='' OR incarnation=?)`,
		policyID,
		claim,
		incarnation,
		incarnation,
	)
	return err
}

func (r *trafficPolicySQLiteRepo) GetVMToFloatingIP(
	ctx context.Context,
	intentID string,
) (*model.VMToFloatingIPTrafficAllowIntent, error) {
	var intent model.VMToFloatingIPTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM vm_floating_ip_traffic_policies WHERE id = ?`, intentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) FindVMToFloatingIPByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.VMToFloatingIPTrafficAllowIntent, error) {
	var intents []*model.VMToFloatingIPTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents,
		`SELECT * FROM vm_floating_ip_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return intents, err
}

func (r *trafficPolicySQLiteRepo) ListVMToFloatingIP(
	ctx context.Context,
) ([]*model.VMToFloatingIPTrafficAllowIntent, error) {
	var intents []*model.VMToFloatingIPTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents, `SELECT * FROM vm_floating_ip_traffic_policies ORDER BY created_at, id`)
	return intents, err
}

// ProjectNetworkToFloatingIP records the ownership receipt returned by root without changing policy authority.
func (r *trafficPolicySQLiteRepo) ProjectNetworkToFloatingIP(
	ctx context.Context,
	intent *model.NetworkToFloatingIPTrafficAllowIntent,
) error {
	result, err := r.db.ExecContext(
		ctx,
		`INSERT INTO network_floating_ip_traffic_policies
 (id,source_network_id,destination_floating_ip_id,protocol,destination_port_start,destination_port_end,
 created_at,updated_at,claim_id,incarnation)
 VALUES (?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET claim_id=excluded.claim_id,
 incarnation=excluded.incarnation,updated_at=excluded.updated_at
 WHERE network_floating_ip_traffic_policies.source_network_id=excluded.source_network_id
 AND network_floating_ip_traffic_policies.destination_floating_ip_id=excluded.destination_floating_ip_id

 AND network_floating_ip_traffic_policies.protocol=excluded.protocol
 AND network_floating_ip_traffic_policies.destination_port_start=excluded.destination_port_start
 AND network_floating_ip_traffic_policies.destination_port_end=excluded.destination_port_end`,
		intent.ID,
		intent.SourceNetworkID,
		intent.DestinationFloatingIPID,
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

// DeleteOwnedNetworkToFloatingIP cannot delete a concurrently replaced local projection.
func (r *trafficPolicySQLiteRepo) DeleteOwnedNetworkToFloatingIP(
	ctx context.Context,
	policyID, claim, incarnation string,
) error {
	_, err := r.db.ExecContext(
		ctx,
		`DELETE FROM network_floating_ip_traffic_policies WHERE id=? AND claim_id=? AND (?='' OR incarnation=?)`,
		policyID,
		claim,
		incarnation,
		incarnation,
	)
	return err
}

func (r *trafficPolicySQLiteRepo) GetNetworkToFloatingIP(
	ctx context.Context,
	intentID string,
) (*model.NetworkToFloatingIPTrafficAllowIntent, error) {
	var intent model.NetworkToFloatingIPTrafficAllowIntent
	err := r.db.GetContext(ctx, &intent, `SELECT * FROM network_floating_ip_traffic_policies WHERE id = ?`, intentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &intent, err
}

func (r *trafficPolicySQLiteRepo) FindNetworkToFloatingIPByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.NetworkToFloatingIPTrafficAllowIntent, error) {
	var intents []*model.NetworkToFloatingIPTrafficAllowIntent
	err := r.db.SelectContext(ctx, &intents,
		`SELECT * FROM network_floating_ip_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return intents, err
}

func (r *trafficPolicySQLiteRepo) ListNetworkToFloatingIP(
	ctx context.Context,
) ([]*model.NetworkToFloatingIPTrafficAllowIntent, error) {
	var intents []*model.NetworkToFloatingIPTrafficAllowIntent
	err := r.db.SelectContext(
		ctx,
		&intents,
		`SELECT * FROM network_floating_ip_traffic_policies ORDER BY created_at, id`,
	)
	return intents, err
}

// InvalidateByFloatingIP removes both destination projections in one transaction after root retirement.
func (r *trafficPolicySQLiteRepo) InvalidateByFloatingIP(ctx context.Context, resourceID string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM vm_floating_ip_traffic_policies WHERE destination_floating_ip_id=?`,
		resourceID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM network_floating_ip_traffic_policies WHERE destination_floating_ip_id=?`,
		resourceID,
	); err != nil {
		return err
	}
	return tx.Commit()
}
