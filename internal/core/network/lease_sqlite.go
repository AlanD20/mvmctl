package network

import (
	"context"
	"database/sql"
	"net"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra/model"
)

type sqliteLeaseRepo struct {
	db *sqlx.DB
}

func NewLeaseRepository(db *sqlx.DB) LeaseRepository {
	return &sqliteLeaseRepo{db: db}
}

func (r *sqliteLeaseRepo) Get(ctx context.Context, networkID, ipv4 string) (*model.NetworkLeaseItem, error) {
	var l model.NetworkLeaseItem
	err := r.db.GetContext(ctx, &l,
		`SELECT * FROM network_leases WHERE network_id = ? AND ipv4 = ?`, networkID, ipv4)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &l, err
}

func (r *sqliteLeaseRepo) ListAll(ctx context.Context, networkID string) ([]*model.NetworkLeaseItem, error) {
	var items []*model.NetworkLeaseItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM network_leases WHERE network_id = ? ORDER BY leased_at`, networkID)
}

func (r *sqliteLeaseRepo) ListByVM(ctx context.Context, networkID, vmID string) ([]*model.NetworkLeaseItem, error) {
	var items []*model.NetworkLeaseItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM network_leases WHERE network_id = ? AND vm_id = ? ORDER BY leased_at`, networkID, vmID)
}

func (r *sqliteLeaseRepo) ListAllBatch(ctx context.Context, networkIDs []string) ([]*model.NetworkLeaseItem, error) {
	if len(networkIDs) == 0 {
		return []*model.NetworkLeaseItem{}, nil
	}
	query, args, err := sqlx.In("SELECT * FROM network_leases WHERE network_id IN (?) ORDER BY leased_at", networkIDs)
	if err != nil {
		return nil, err
	}
	query = r.db.Rebind(query)
	var items []*model.NetworkLeaseItem
	return items, r.db.SelectContext(ctx, &items, query, args...)
}

func (r *sqliteLeaseRepo) Acquire(ctx context.Context, networkID, ipv4 string, vmID *string) (*model.NetworkLeaseItem, error) {
	result, err := r.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
		networkID, ipv4, vmID)
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, nil
	}
	return r.Get(ctx, networkID, ipv4)
}

func (r *sqliteLeaseRepo) Release(ctx context.Context, networkID, ipv4 string) error {
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM network_leases WHERE network_id = ? AND ipv4 = ?", networkID, ipv4)
	return err
}

func (r *sqliteLeaseRepo) ReleaseByVM(ctx context.Context, vmID string) error {
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM network_leases WHERE vm_id = ?", vmID)
	return err
}

func (r *sqliteLeaseRepo) Count(ctx context.Context) (int, error) {
	var c int
	return c, sqlx.GetContext(ctx, r.db, &c, "SELECT COUNT(*) FROM network_leases")
}

func (r *sqliteLeaseRepo) CountAvailable(ctx context.Context, networkID string) (int, error) {
	type netInfo struct {
		Subnet      string `db:"subnet"`
		IPv4Gateway string `db:"ipv4_gateway"`
	}
	var info netInfo
	err := sqlx.GetContext(ctx, r.db, &info,
		"SELECT subnet, ipv4_gateway FROM networks WHERE id = ? AND deleted_at IS NULL AND is_present = 1",
		networkID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	subnet, gateway := info.Subnet, info.IPv4Gateway

	var leaseCount int
	if err := sqlx.GetContext(ctx, r.db, &leaseCount,
		"SELECT COUNT(*) FROM network_leases WHERE network_id = ?", networkID); err != nil {
		return 0, err
	}

	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return 0, err
	}
	totalHosts := countHosts(ipnet)
	gatewayCount := 0
	if gateway != "" {
		gatewayCount = 1
	}
	available := totalHosts - gatewayCount - leaseCount
	if available < 0 {
		available = 0
	}
	return available, nil
}

func countHosts(ipnet *net.IPNet) int {
	ip := ipnet.IP.To4()
	if ip == nil {
		return 0
	}
	ones, bits := ipnet.Mask.Size()
	total := 1 << (bits - ones)
	if total <= 2 {
		return total
	}
	return total - 2
}
