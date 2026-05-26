package network

import (
	"context"
	"database/sql"
	"fmt"
	"mvmctl/internal/infra"
	"strings"
	"time"
)

type sqliteRepo struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository {
	return &sqliteRepo{db: db}
}

// ── Repository implementation ──

func (r *sqliteRepo) Get(ctx context.Context, networkID string) (*Network, error) {
	row := r.db.QueryRowContext(ctx, `SELECT * FROM networks WHERE id = ? AND deleted_at IS NULL AND is_present = 1`, networkID)
	return scanNetwork(row)
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*Network, error) {
	row := r.db.QueryRowContext(ctx, `SELECT * FROM networks WHERE name = ? AND deleted_at IS NULL AND is_present = 1`, name)
	return scanNetwork(row)
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*Network, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM networks WHERE id LIKE ? AND deleted_at IS NULL AND is_present = 1`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("find networks by prefix '%s': %w", prefix, err)
	}
	defer rows.Close()
	return scanNetworks(rows)
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var c int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM networks WHERE deleted_at IS NULL").Scan(&c)
	if err != nil {
		return 0, err
	}
	return c, nil
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*Network, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM networks WHERE deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list all networks: %w", err)
	}
	defer rows.Close()
	return scanNetworks(rows)
}

func (r *sqliteRepo) Upsert(ctx context.Context, n *Network) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO networks (
			id, name, subnet, bridge, ipv4_gateway, bridge_active,
			nat_gateways, nat_enabled, is_default, is_present,
			created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			subnet = excluded.subnet,
			bridge = excluded.bridge,
			ipv4_gateway = excluded.ipv4_gateway,
			bridge_active = excluded.bridge_active,
			nat_gateways = excluded.nat_gateways,
			nat_enabled = excluded.nat_enabled,
			is_default = excluded.is_default,
			is_present = excluded.is_present,
			updated_at = CURRENT_TIMESTAMP,
			deleted_at = excluded.deleted_at
	`,
		n.ID, n.Name, n.Subnet, n.Bridge, n.IPv4Gateway,
		infra.BoolToInt(n.BridgeActive), n.NATGateways, infra.BoolToInt(n.NATEnabled),
		infra.BoolToInt(n.IsDefault), infra.BoolToInt(n.IsPresent),
		n.CreatedAt, n.UpdatedAt, n.DeletedAt)
	return err
}

func (r *sqliteRepo) UpdateBridgeActive(ctx context.Context, networkID string, active bool) error {
	_, err := r.db.ExecContext(ctx, "UPDATE networks SET bridge_active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		infra.BoolToInt(active), networkID)
	return err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, networkID string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE networks SET is_default = 0 WHERE deleted_at IS NULL"); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE networks SET is_default = 1 WHERE id = ? AND deleted_at IS NULL", networkID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *sqliteRepo) GetDefault(ctx context.Context) (*Network, error) {
	row := r.db.QueryRowContext(ctx, `SELECT * FROM networks WHERE is_default = 1 AND deleted_at IS NULL AND is_present = 1 LIMIT 1`)
	return scanNetwork(row)
}

func (r *sqliteRepo) UpdateManyIsPresent(ctx context.Context, networkIDs []string, isPresent bool) error {
	if len(networkIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(networkIDs))
	args := make([]interface{}, 0, len(networkIDs)+1)
	args = append(args, infra.BoolToInt(isPresent))
	for i, id := range networkIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf("UPDATE networks SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	_, err := r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *sqliteRepo) SoftDelete(ctx context.Context, networkID string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx, "UPDATE networks SET deleted_at = ?, is_present = 0 WHERE id = ?", now, networkID)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, networkID string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM networks WHERE id = ?", networkID)
	return err
}

// ── Scan helpers ──

func scanNetwork(row *sql.Row) (*Network, error) {
	var n Network
	var natGateways, deletedAt sql.NullString
	var bridgeActive, natEnabled, isDefault, isPresent int
	err := row.Scan(&n.ID, &n.Name, &n.Subnet, &n.Bridge, &n.IPv4Gateway,
		&bridgeActive, &natGateways, &natEnabled, &isDefault, &isPresent,
		&n.CreatedAt, &n.UpdatedAt, &deletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.BridgeActive = bridgeActive != 0
	n.NATEnabled = natEnabled != 0
	n.IsDefault = isDefault != 0
	n.IsPresent = isPresent != 0
	if natGateways.Valid {
		n.NATGateways = &natGateways.String
	}
	if deletedAt.Valid && deletedAt.String != "" {
		n.DeletedAt = &deletedAt.String
	}
	return &n, nil
}

func scanNetworks(rows *sql.Rows) ([]*Network, error) {
	var networks []*Network
	for rows.Next() {
		var n Network
		var natGateways, deletedAt sql.NullString
		var bridgeActive, natEnabled, isDefault, isPresent int
		err := rows.Scan(&n.ID, &n.Name, &n.Subnet, &n.Bridge, &n.IPv4Gateway,
			&bridgeActive, &natGateways, &natEnabled, &isDefault, &isPresent,
			&n.CreatedAt, &n.UpdatedAt, &deletedAt)
		if err != nil {
			return nil, err
		}
		n.BridgeActive = bridgeActive != 0
		n.NATEnabled = natEnabled != 0
		n.IsDefault = isDefault != 0
		n.IsPresent = isPresent != 0
		if natGateways.Valid {
			n.NATGateways = &natGateways.String
		}
		if deletedAt.Valid && deletedAt.String != "" {
			n.DeletedAt = &deletedAt.String
		}
		networks = append(networks, &n)
	}
	return networks, rows.Err()
}
