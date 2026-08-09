package network

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

type serviceAccessPolicySQLiteRepo struct {
	db *sqlx.DB
}

// NewServiceAccessPolicyRepository creates the SQLite policy repository.
func NewServiceAccessPolicyRepository(db *sqlx.DB) ServiceAccessPolicyRepository {
	return &serviceAccessPolicySQLiteRepo{db: db}
}

func (r *serviceAccessPolicySQLiteRepo) Create(ctx context.Context, policy *model.ServiceAccessPolicy) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO service_access_policies (
			id, source_network_id, destination_vm_id, protocol,
			destination_port_start, destination_port_end, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		policy.ID, policy.SourceNetworkID, policy.DestinationVMID, string(policy.Protocol),
		policy.DestinationPortStart, policy.DestinationPortEnd, policy.CreatedAt, policy.UpdatedAt,
	)
	return err
}

func (r *serviceAccessPolicySQLiteRepo) Get(
	ctx context.Context,
	policyID string,
) (*model.ServiceAccessPolicy, error) {
	var policy model.ServiceAccessPolicy
	err := r.db.GetContext(ctx, &policy, `SELECT * FROM service_access_policies WHERE id = ?`, policyID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &policy, err
}

func (r *serviceAccessPolicySQLiteRepo) GetByIdentity(
	ctx context.Context,
	identity *model.ServiceAccessPolicy,
) (*model.ServiceAccessPolicy, error) {
	var policy model.ServiceAccessPolicy
	err := r.db.GetContext(ctx, &policy, `
		SELECT * FROM service_access_policies
		WHERE source_network_id = ? AND destination_vm_id = ? AND protocol = ?
		  AND destination_port_start = ? AND destination_port_end = ?`,
		identity.SourceNetworkID, identity.DestinationVMID, string(identity.Protocol),
		identity.DestinationPortStart, identity.DestinationPortEnd)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &policy, err
}

func (r *serviceAccessPolicySQLiteRepo) FindByPrefix(
	ctx context.Context,
	prefix string,
) ([]*model.ServiceAccessPolicy, error) {
	var policies []*model.ServiceAccessPolicy
	err := r.db.SelectContext(
		ctx,
		&policies,
		`SELECT * FROM service_access_policies WHERE id LIKE ? ORDER BY created_at, id`,
		prefix+"%",
	)
	return policies, err
}

func (r *serviceAccessPolicySQLiteRepo) List(ctx context.Context) ([]*model.ServiceAccessPolicy, error) {
	var policies []*model.ServiceAccessPolicy
	err := r.db.SelectContext(
		ctx,
		&policies,
		`SELECT * FROM service_access_policies ORDER BY created_at, id`,
	)
	return policies, err
}

func (r *serviceAccessPolicySQLiteRepo) Delete(ctx context.Context, policyID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM service_access_policies WHERE id = ?`, policyID)
	return err
}

func (r *serviceAccessPolicySQLiteRepo) DeleteMany(ctx context.Context, policyIDs []string) error {
	if len(policyIDs) == 0 {
		return nil
	}
	query, args, err := sqlx.In(`DELETE FROM service_access_policies WHERE id IN (?)`, policyIDs)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, r.db.Rebind(query), args...); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *serviceAccessPolicySQLiteRepo) DeleteByVM(ctx context.Context, vmID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM service_access_policies WHERE destination_vm_id = ?`, vmID)
	return err
}

func (r *serviceAccessPolicySQLiteRepo) DeleteBySourceNetwork(ctx context.Context, networkID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM service_access_policies WHERE source_network_id = ?`, networkID)
	return err
}
