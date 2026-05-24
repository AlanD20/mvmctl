package network

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
)

type sqliteLeaseRepo struct {
	db *sql.DB
}

func NewLeaseRepository(db *sql.DB) LeaseRepository {
	return &sqliteLeaseRepo{db: db}
}

func (r *sqliteLeaseRepo) Get(ctx context.Context, networkID, ipv4 string) (*NetworkLeaseItem, error) {
	row := r.db.QueryRowContext(ctx, `SELECT * FROM network_leases WHERE network_id = ? AND ipv4 = ?`, networkID, ipv4)
	return scanLeaseItem(row)
}

func (r *sqliteLeaseRepo) ListAll(ctx context.Context, networkID string) ([]*NetworkLeaseItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM network_leases WHERE network_id = ? ORDER BY leased_at`, networkID)
	if err != nil {
		return nil, fmt.Errorf("list leases for network %s: %w", networkID, err)
	}
	defer rows.Close()
	return scanLeaseItems(rows)
}

func (r *sqliteLeaseRepo) ListByVM(ctx context.Context, networkID, vmID string) ([]*NetworkLeaseItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM network_leases WHERE network_id = ? AND vm_id = ? ORDER BY leased_at`, networkID, vmID)
	if err != nil {
		return nil, fmt.Errorf("list leases for network %s vm %s: %w", networkID, vmID, err)
	}
	defer rows.Close()
	return scanLeaseItems(rows)
}

func (r *sqliteLeaseRepo) ListAllBatch(ctx context.Context, networkIDs []string) ([]*NetworkLeaseItem, error) {
	if len(networkIDs) == 0 {
		return []*NetworkLeaseItem{}, nil
	}
	placeholders := make([]string, len(networkIDs))
	args := make([]interface{}, len(networkIDs))
	for i, id := range networkIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("SELECT * FROM network_leases WHERE network_id IN (%s) ORDER BY leased_at",
		strings.Join(placeholders, ","))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list leases batch: %w", err)
	}
	defer rows.Close()
	return scanLeaseItems(rows)
}

func (r *sqliteLeaseRepo) Acquire(ctx context.Context, networkID, ipv4 string, vmID *string) (*NetworkLeaseItem, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.Exec("INSERT INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
		networkID, ipv4, vmID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	lease, err := r.Get(ctx, networkID, ipv4)
	if err != nil {
		return nil, err
	}
	return lease, nil
}

func (r *sqliteLeaseRepo) Release(ctx context.Context, networkID, ipv4 string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM network_leases WHERE network_id = ? AND ipv4 = ?", networkID, ipv4)
	return err
}

func (r *sqliteLeaseRepo) ReleaseByVM(ctx context.Context, vmID string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM network_leases WHERE vm_id = ?", vmID)
	return err
}

func (r *sqliteLeaseRepo) Count(ctx context.Context) (int, error) {
	var c int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM network_leases").Scan(&c)
	if err != nil {
		return 0, err
	}
	return c, nil
}

func (r *sqliteLeaseRepo) CountAvailable(ctx context.Context, networkID string) (int, error) {
	row := r.db.QueryRowContext(ctx, "SELECT subnet, ipv4_gateway FROM networks WHERE id = ? AND deleted_at IS NULL AND is_present = 1", networkID)
	var subnet, gateway string
	if err := row.Scan(&subnet, &gateway); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}

	var leaseCount int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM network_leases WHERE network_id = ?", networkID).Scan(&leaseCount)
	if err != nil {
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

// countHosts returns the number of usable host addresses in the network.
// Matches Python's len(list(ipaddress.IPv4Network(subnet, strict=False).hosts())).
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

// ── Scan helpers ──

func scanLeaseItem(row *sql.Row) (*NetworkLeaseItem, error) {
	var l NetworkLeaseItem
	var vmID, expiresAt sql.NullString
	var id sql.NullInt64
	err := row.Scan(&id, &l.NetworkID, &l.IPv4, &vmID, &l.LeasedAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if id.Valid {
		l.ID = &id.Int64
	}
	if vmID.Valid {
		l.VMID = &vmID.String
	}
	if expiresAt.Valid {
		l.ExpiresAt = &expiresAt.String
	}
	return &l, nil
}

func scanLeaseItems(rows *sql.Rows) ([]*NetworkLeaseItem, error) {
	var items []*NetworkLeaseItem
	for rows.Next() {
		var l NetworkLeaseItem
		var vmID, expiresAt sql.NullString
		var id sql.NullInt64
		err := rows.Scan(&id, &l.NetworkID, &l.IPv4, &vmID, &l.LeasedAt, &expiresAt)
		if err != nil {
			return nil, err
		}
		if id.Valid {
			l.ID = &id.Int64
		}
		if vmID.Valid {
			l.VMID = &vmID.String
		}
		if expiresAt.Valid {
			l.ExpiresAt = &expiresAt.String
		}
		items = append(items, &l)
	}
	return items, rows.Err()
}
