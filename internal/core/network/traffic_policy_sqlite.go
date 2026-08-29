package network

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

type networkTrafficPolicySQLiteRepo struct{ db *sqlx.DB }
type vmTrafficPolicySQLiteRepo struct{ db *sqlx.DB }

// NewNetworkTrafficPolicyRepository creates the SQLite network-source policy repository.
func NewNetworkTrafficPolicyRepository(db *sqlx.DB) NetworkTrafficPolicyRepository {
	return &networkTrafficPolicySQLiteRepo{db: db}
}

// NewVMTrafficPolicyRepository creates the SQLite exact-VM-source policy repository.
func NewVMTrafficPolicyRepository(db *sqlx.DB) VMTrafficPolicyRepository {
	return &vmTrafficPolicySQLiteRepo{db: db}
}

func (r *networkTrafficPolicySQLiteRepo) Create(ctx context.Context, policy *model.NetworkTrafficPolicy) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO network_traffic_policies
		(id, source_network_id, destination_vm_id, protocol, destination_port_start,
		 destination_port_end, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		policy.ID, policy.SourceNetworkID, policy.DestinationVMID, string(policy.Protocol),
		policy.DestinationPortStart, policy.DestinationPortEnd, policy.CreatedAt, policy.UpdatedAt)
	return err
}

func (r *networkTrafficPolicySQLiteRepo) Get(
	ctx context.Context,
	policyID string,
) (*model.NetworkTrafficPolicy, error) {
	var policy model.NetworkTrafficPolicy
	err := r.db.GetContext(ctx, &policy, `SELECT * FROM network_traffic_policies WHERE id = ?`, policyID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &policy, err
}

func (r *networkTrafficPolicySQLiteRepo) GetByIdentity(
	ctx context.Context,
	identity *model.NetworkTrafficPolicy,
) (*model.NetworkTrafficPolicy, error) {
	var policy model.NetworkTrafficPolicy
	err := r.db.GetContext(ctx, &policy, `SELECT * FROM network_traffic_policies
		WHERE source_network_id = ? AND destination_vm_id = ? AND protocol = ?
		AND destination_port_start = ? AND destination_port_end = ?`,
		identity.SourceNetworkID, identity.DestinationVMID, string(identity.Protocol),
		identity.DestinationPortStart, identity.DestinationPortEnd)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &policy, err
}

func (r *networkTrafficPolicySQLiteRepo) FindByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.NetworkTrafficPolicy, error) {
	var policies []*model.NetworkTrafficPolicy
	err := r.db.SelectContext(ctx, &policies,
		`SELECT * FROM network_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return policies, err
}

func (r *networkTrafficPolicySQLiteRepo) List(ctx context.Context) ([]*model.NetworkTrafficPolicy, error) {
	var policies []*model.NetworkTrafficPolicy
	err := r.db.SelectContext(ctx, &policies,
		`SELECT * FROM network_traffic_policies ORDER BY created_at, id`)
	return policies, err
}

func (r *networkTrafficPolicySQLiteRepo) DeleteMany(ctx context.Context, policyIDs []string) error {
	return deleteTrafficPolicies(ctx, r.db, "network_traffic_policies", policyIDs)
}

func (r *networkTrafficPolicySQLiteRepo) DeleteByVM(ctx context.Context, vmID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM network_traffic_policies WHERE destination_vm_id = ?`, vmID)
	return err
}

func (r *networkTrafficPolicySQLiteRepo) DeleteByNetwork(ctx context.Context, networkID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM network_traffic_policies WHERE source_network_id = ?`, networkID)
	return err
}

func (r *vmTrafficPolicySQLiteRepo) Create(ctx context.Context, policy *model.VMTrafficPolicy) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO vm_traffic_policies
		(id, source_vm_id, destination_vm_id, protocol, destination_port_start,
		 destination_port_end, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		policy.ID, policy.SourceVMID, policy.DestinationVMID, string(policy.Protocol),
		policy.DestinationPortStart, policy.DestinationPortEnd, policy.CreatedAt, policy.UpdatedAt)
	return err
}

func (r *vmTrafficPolicySQLiteRepo) Get(ctx context.Context, policyID string) (*model.VMTrafficPolicy, error) {
	var policy model.VMTrafficPolicy
	err := r.db.GetContext(ctx, &policy, `SELECT * FROM vm_traffic_policies WHERE id = ?`, policyID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &policy, err
}

func (r *vmTrafficPolicySQLiteRepo) GetByIdentity(
	ctx context.Context,
	identity *model.VMTrafficPolicy,
) (*model.VMTrafficPolicy, error) {
	var policy model.VMTrafficPolicy
	err := r.db.GetContext(ctx, &policy, `SELECT * FROM vm_traffic_policies
		WHERE source_vm_id = ? AND destination_vm_id = ? AND protocol = ?
		AND destination_port_start = ? AND destination_port_end = ?`,
		identity.SourceVMID, identity.DestinationVMID, string(identity.Protocol),
		identity.DestinationPortStart, identity.DestinationPortEnd)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &policy, err
}

func (r *vmTrafficPolicySQLiteRepo) FindByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.VMTrafficPolicy, error) {
	var policies []*model.VMTrafficPolicy
	err := r.db.SelectContext(ctx, &policies,
		`SELECT * FROM vm_traffic_policies WHERE id LIKE ? ORDER BY created_at, id`, prefix+"%")
	return policies, err
}

func (r *vmTrafficPolicySQLiteRepo) List(ctx context.Context) ([]*model.VMTrafficPolicy, error) {
	var policies []*model.VMTrafficPolicy
	err := r.db.SelectContext(ctx, &policies, `SELECT * FROM vm_traffic_policies ORDER BY created_at, id`)
	return policies, err
}

func (r *vmTrafficPolicySQLiteRepo) DeleteMany(ctx context.Context, policyIDs []string) error {
	return deleteTrafficPolicies(ctx, r.db, "vm_traffic_policies", policyIDs)
}

func (r *vmTrafficPolicySQLiteRepo) DeleteByVM(ctx context.Context, vmID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM vm_traffic_policies WHERE source_vm_id = ? OR destination_vm_id = ?`, vmID, vmID)
	return err
}

func deleteTrafficPolicies(ctx context.Context, db *sqlx.DB, table string, policyIDs []string) error {
	if len(policyIDs) == 0 {
		return nil
	}
	query, args, err := sqlx.In(`DELETE FROM `+table+` WHERE id IN (?)`, policyIDs)
	if err != nil {
		return err
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, db.Rebind(query), args...); err != nil {
		return err
	}
	return tx.Commit()
}
