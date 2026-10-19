package network

import (
	"context"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ProjectNetworkToVM records the ownership receipt returned by root without changing policy authority.
func (r *trafficPolicySQLiteRepo) ProjectNetworkToVM(
	ctx context.Context,
	intent *model.NetworkToVMTrafficAllowIntent,
) error {
	result, err := r.db.ExecContext(
		ctx,
		`INSERT INTO network_traffic_policies
 (id,source_network_id,destination_vm_id,protocol,destination_port_start,destination_port_end,created_at,updated_at,claim_id,incarnation)
 VALUES (?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET claim_id=excluded.claim_id,incarnation=excluded.incarnation,updated_at=excluded.updated_at
 WHERE network_traffic_policies.source_network_id=excluded.source_network_id AND network_traffic_policies.destination_vm_id=excluded.destination_vm_id
 AND network_traffic_policies.protocol=excluded.protocol AND network_traffic_policies.destination_port_start=excluded.destination_port_start
 AND network_traffic_policies.destination_port_end=excluded.destination_port_end`,
		intent.ID,
		intent.SourceNetworkID,
		intent.DestinationVMID,
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

// DeleteOwnedNetworkToVM cannot delete a concurrently replaced local projection.
func (r *trafficPolicySQLiteRepo) DeleteOwnedNetworkToVM(
	ctx context.Context,
	policyID, claim, incarnation string,
) error {
	_, err := r.db.ExecContext(
		ctx,
		`DELETE FROM network_traffic_policies WHERE id=? AND claim_id=? AND (?='' OR incarnation=?)`,
		policyID,
		claim,
		incarnation,
		incarnation,
	)
	return err
}

// ProjectVMToVM records the ownership receipt returned by root without changing policy authority.
func (r *trafficPolicySQLiteRepo) ProjectVMToVM(ctx context.Context, intent *model.VMToVMTrafficAllowIntent) error {
	result, err := r.db.ExecContext(
		ctx,
		`INSERT INTO vm_traffic_policies
 (id,source_vm_id,destination_vm_id,protocol,destination_port_start,destination_port_end,created_at,updated_at,claim_id,incarnation)
 VALUES (?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET claim_id=excluded.claim_id,incarnation=excluded.incarnation,updated_at=excluded.updated_at
 WHERE vm_traffic_policies.source_vm_id=excluded.source_vm_id AND vm_traffic_policies.destination_vm_id=excluded.destination_vm_id
 AND vm_traffic_policies.protocol=excluded.protocol AND vm_traffic_policies.destination_port_start=excluded.destination_port_start
 AND vm_traffic_policies.destination_port_end=excluded.destination_port_end`,
		intent.ID,
		intent.SourceVMID,
		intent.DestinationVMID,
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

// DeleteOwnedVMToVM cannot delete a concurrently replaced local projection.
func (r *trafficPolicySQLiteRepo) DeleteOwnedVMToVM(ctx context.Context, policyID, claim, incarnation string) error {
	_, err := r.db.ExecContext(
		ctx,
		`DELETE FROM vm_traffic_policies WHERE id=? AND claim_id=? AND (?='' OR incarnation=?)`,
		policyID,
		claim,
		incarnation,
		incarnation,
	)
	return err
}
