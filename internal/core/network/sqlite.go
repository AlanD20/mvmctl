package network

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &sqliteRepo{db: db}
}

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.Network, error) {
	var n model.Network
	err := r.db.GetContext(ctx, &n,
		`SELECT * FROM networks WHERE id = ? AND deleted_at IS NULL`, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &n, err
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.Network, error) {
	var n model.Network
	err := r.db.GetContext(ctx, &n,
		`SELECT * FROM networks WHERE name = ? AND deleted_at IS NULL`, name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &n, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.Network, error) {
	var items []*model.Network
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM networks WHERE id LIKE ? AND deleted_at IS NULL`, prefix+"%")
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.Network, error) {
	var items []*model.Network
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM networks WHERE deleted_at IS NULL ORDER BY created_at`)
}

func (r *sqliteRepo) Upsert(ctx context.Context, n *model.Network) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO networks (
			id, name, subnet, bridge, ipv4_gateway,
			bridge_active, nat_gateways, nat_enabled,
			is_default, is_present, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			subnet = excluded.subnet,
			bridge = excluded.bridge,
			ipv4_gateway = excluded.ipv4_gateway,
			bridge_active = excluded.bridge_active,
			nat_gateways = excluded.nat_gateways,
			nat_enabled = excluded.nat_enabled,
			is_default = excluded.is_default,
			is_present = excluded.is_present,
			updated_at = CURRENT_TIMESTAMP,
			deleted_at = excluded.deleted_at`,
		n.ID, n.Name, n.Subnet, n.Bridge, n.IPv4Gateway,
		infra.BoolToInt(n.BridgeActive), n.NATGateways, infra.BoolToInt(n.NATEnabled),
		infra.BoolToInt(n.IsDefault), infra.BoolToInt(n.IsPresent),
		n.CreatedAt, n.UpdatedAt, n.DeletedAt,
	)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM networks WHERE id = ?`, id)
	return err
}

func (r *sqliteRepo) SoftDelete(ctx context.Context, id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx,
		`UPDATE networks SET deleted_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, now, id)
	return err
}

func (r *sqliteRepo) UpdateManyIsPresent(ctx context.Context, ids []string, present bool) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(ids)+1)
	args = append(args, infra.BoolToInt(present))
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE networks SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (`+placeholders+`)`,
		args...)
	return err
}

func (r *sqliteRepo) UpdateBridgeActive(ctx context.Context, networkID string, active bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE networks SET bridge_active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		infra.BoolToInt(active), networkID)
	return err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, networkID string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`UPDATE networks SET is_default = 0 WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE networks SET is_default = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND deleted_at IS NULL`,
		networkID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (r *sqliteRepo) GetDefault(ctx context.Context) (*model.Network, error) {
	var n model.Network
	err := r.db.GetContext(ctx, &n,
		`SELECT * FROM networks WHERE is_default = 1 AND deleted_at IS NULL LIMIT 1`)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &n, err
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	err := sqlx.GetContext(ctx, r.db, &count, `SELECT COUNT(*) FROM networks WHERE deleted_at IS NULL`)
	return count, err
}
